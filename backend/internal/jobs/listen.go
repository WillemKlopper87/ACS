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
// version of the same "acquire a dedicated connection, unwrap to the
// native driver conn" pattern; this is the long-lived version of the
// same idea.
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
// connection back to the pool. It blocks until the goroutine has
// actually exited, so a caller observing Close's return (or
// Notifications() closing, which happens first) knows the goroutine is
// gone -- no leak, no lingering connection.
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
// (connection dropped, network blip) it closes the broken connection,
// backs off, reacquires a fresh connection re-issuing LISTEN, and
// resumes. Only ctx cancellation (via Close) stops it for good.
func (l *QueueListener) run(ctx context.Context, conn *sql.Conn, native *pgx.Conn) {
	defer close(l.done)
	defer close(l.notifications)

	for {
		notification, err := native.WaitForNotification(ctx)
		if err != nil {
			conn.Close()
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

		l.deliver(notification.Payload)
	}
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
