package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// NotifyChannel is the Postgres NOTIFY channel a job lands on when it's
// queued -- migrations/0054_jobs_notify.sql's jobs_notify_queued trigger
// fires pg_notify on this exact channel name whenever a jobs row is
// inserted with status = 'QUEUED'. The migration file can't reference
// this Go constant (it's plain SQL), so the two must be kept identical
// by hand; TestListenReceivesNotification listens on this constant and
// fails (by timing out) if it and the migration's literal string ever
// drift.
const NotifyChannel = "acs_jobs_queued"

// notifyBufferSize sizes Notifications()'s channel. A burst of job
// inserts shouldn't block Postgres's own notify delivery, so the buffer
// absorbs a reasonable burst; if it ever fills, the oldest unread
// notification is dropped (with a logged warning) rather than blocking.
// No job is lost this way -- only its "hurry up" signal -- and the
// periodic sweep (build plan Task 4) is the stated safety net for
// exactly that gap.
const notifyBufferSize = 64

// Reconnect backoff: start at 1s, double each failed attempt, cap at
// 30s. Nothing fancier is called for by the contract.
const (
	reconnectInitialBackoff = 1 * time.Second
	reconnectMaxBackoff     = 30 * time.Second
)

// QueueListener listens for Postgres NOTIFY messages on one channel and
// republishes each notification's payload (a device id) on
// Notifications(). It holds one dedicated *sql.Conn for its entire
// life -- see internal/store/postgres.go's Migrate for the one-shot
// version of the same "acquire a dedicated connection via db.Conn,
// hold it, eventually Close it" pattern. Migrate stops there (it never
// unwraps to the native driver conn); the unwrap-to-*pgx.Conn step below
// is specific to this listener, since Migrate has no need to reach
// WaitForNotification.
type QueueListener struct {
	db      *sql.DB
	channel string
	log     *slog.Logger

	notifications chan string
	cancel        context.CancelFunc
	done          chan struct{}
}

// Listen acquires one dedicated connection from db, issues
// LISTEN <channel> on it, and starts a background goroutine that
// delivers notification payloads to Notifications(). The goroutine keeps
// running -- reconnecting with a capped, doubling backoff after any
// error -- until Close is called or ctx is canceled; only that stops it
// for good.
func Listen(ctx context.Context, db *sql.DB, channel string, log *slog.Logger) (*QueueListener, error) {
	if log == nil {
		log = slog.Default()
	}

	conn, native, err := acquireListenConn(ctx, db, channel)
	if err != nil {
		return nil, err
	}

	listenCtx, cancel := context.WithCancel(ctx)
	l := &QueueListener{
		db:            db,
		channel:       channel,
		log:           log,
		notifications: make(chan string, notifyBufferSize),
		cancel:        cancel,
		done:          make(chan struct{}),
	}

	go l.run(listenCtx, conn, native)

	return l, nil
}

// Notifications returns the channel of device_id payload strings
// delivered from Postgres NOTIFY messages received on l's channel.
func (l *QueueListener) Notifications() <-chan string {
	return l.notifications
}

// Close stops l's background goroutine and releases its dedicated
// connection back to the pool. Canceling the listener's context alone
// does not end the connection's LISTEN subscription -- pgconn's
// deadline-based cancel watcher does not close the underlying connection
// for a timeout-classified error, so the connection comes back healthy
// and still subscribed; run's shutdown path issues a best-effort
// UNLISTEN (see releaseConn) before returning the connection to the pool
// so a future borrower of that pooled connection doesn't silently
// accumulate undrained notifications. Close blocks until the goroutine
// has actually exited, so a caller observing Close's return (or
// Notifications() closing, which happens first) knows the goroutine --
// and its connection release -- are done.
func (l *QueueListener) Close() error {
	l.cancel()
	<-l.done
	return nil
}

// acquireListenConn acquires one dedicated *sql.Conn from db, issues
// LISTEN <channel> on it via database/sql's normal Exec (simpler than
// doing it through the raw driver conn too), and unwraps that connection
// to the native *pgx.Conn that WaitForNotification needs --
// database/sql has no equivalent of its own.
func acquireListenConn(ctx context.Context, db *sql.DB, channel string) (*sql.Conn, *pgx.Conn, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("acquire listen connection: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "LISTEN "+channel); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("LISTEN %s: %w", channel, err)
	}

	var native *pgx.Conn
	if err := conn.Raw(func(driverConn any) error {
		sc, ok := driverConn.(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("driver conn is %T, want *stdlib.Conn", driverConn)
		}
		native = sc.Conn()
		return nil
	}); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("unwrap listen connection: %w", err)
	}

	return conn, native, nil
}

// run is the listener's background goroutine. It blocks on
// WaitForNotification and republishes each payload; on any error
// (connection dropped, network blip, or ctx canceled via Close) it
// releases the current connection (see releaseConn) and, unless that
// error was a shutdown, backs off, reacquires a fresh connection
// re-issuing LISTEN, and resumes. Only ctx cancellation (via Close)
// stops it for good.
func (l *QueueListener) run(ctx context.Context, conn *sql.Conn, native *pgx.Conn) {
	defer close(l.done)
	defer close(l.notifications)

	for {
		notification, err := native.WaitForNotification(ctx)
		// WaitForNotification can return a non-nil notification alongside a
		// non-nil error (a notification already buffered when the call
		// errors) -- delivering it before handling the error means a
		// buffered-then-error notification is never silently discarded.
		if notification != nil {
			l.deliver(notification.Payload)
		}
		if err != nil {
			l.releaseConn(conn)
			if ctx.Err() != nil {
				return
			}
			l.log.Warn("jobs: notification listener lost its connection, reconnecting",
				"channel", l.channel, "error", err)

			var ok bool
			conn, native, ok = l.reconnect(ctx)
			if !ok {
				return
			}
			continue
		}
	}
}

// releaseConn returns conn to db's pool, first issuing a best-effort
// UNLISTEN. This matters specifically on the shutdown path: canceling
// the listener's context interrupts WaitForNotification through
// pgconn's deadline-based cancel watcher, which for a timeout-classified
// error does NOT close the underlying connection (peekMessage skips
// asyncClose, and HandleUnwatchAfterCancel clears the deadline) -- so
// the pgx connection comes back healthy and still subscribed to
// l.channel. Without the UNLISTEN, conn.Close() below would hand that
// still-listening session back to *sql.DB's pool -- stdlib.Conn's
// ResetSession never issues UNLISTEN itself -- and every subsequent job
// insert would deliver a NotificationResponse onto that pooled
// connection's internal notification buffer, which nothing drains,
// growing unbounded until ConnMaxLifetime eventually retires the
// connection. UNLISTEN is best-effort and given its own short-lived
// context (ctx may already be canceled): on a genuine connection failure
// (the other way this is reached) it simply fails fast and is logged,
// and conn.Close() discards the connection either way.
func (l *QueueListener) releaseConn(conn *sql.Conn) {
	unlistenCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := conn.ExecContext(unlistenCtx, "UNLISTEN "+l.channel); err != nil {
		l.log.Warn("jobs: UNLISTEN before releasing listen connection failed (best effort, proceeding)",
			"channel", l.channel, "error", err)
	}
	conn.Close()
}

// reconnect retries acquireListenConn with a doubling, capped backoff
// until it succeeds or ctx is canceled (in which case ok is false and
// run must stop).
func (l *QueueListener) reconnect(ctx context.Context) (*sql.Conn, *pgx.Conn, bool) {
	backoff := reconnectInitialBackoff
	for {
		select {
		case <-ctx.Done():
			return nil, nil, false
		case <-time.After(backoff):
		}

		conn, native, err := acquireListenConn(ctx, l.db, l.channel)
		if err == nil {
			return conn, native, true
		}
		if ctx.Err() != nil {
			return nil, nil, false
		}
		l.log.Warn("jobs: failed to reacquire listen connection, retrying",
			"channel", l.channel, "error", err, "backoff", backoff)

		backoff *= 2
		if backoff > reconnectMaxBackoff {
			backoff = reconnectMaxBackoff
		}
	}
}

// deliver publishes payload on l.notifications, dropping the oldest
// unread notification (with a logged warning) instead of blocking when
// the buffer is full -- see notifyBufferSize's doc comment for why
// that's an accepted, bounded degradation rather than data loss.
func (l *QueueListener) deliver(payload string) {
	select {
	case l.notifications <- payload:
		return
	default:
	}

	select {
	case old := <-l.notifications:
		l.log.Warn("jobs: notification buffer full, dropped oldest pending notification",
			"channel", l.channel, "dropped_device_id", old)
	default:
	}

	select {
	case l.notifications <- payload:
	default:
		// Another goroutine can't be racing us (single producer), but stay
		// defensive rather than block if this is ever reached.
	}
}
