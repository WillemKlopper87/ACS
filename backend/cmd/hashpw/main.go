// hashpw prints a bcrypt hash for a password, at the same cost
// bootstrapAdmin/operators.Create use — for manually resetting an
// operator's password_hash directly in Postgres when the normal
// bootstrap-on-empty-table path doesn't apply (e.g. the operators table
// already has a row from an earlier run with a since-forgotten
// password). See scripts/reset-admin-password.sh, which wraps this.
package main

import (
	"fmt"
	"os"

	"golang.org/x/crypto/bcrypt"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: hashpw <password>")
		os.Exit(1)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(os.Args[1]), bcrypt.DefaultCost)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Println(string(hash))
}
