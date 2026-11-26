package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"syscall"

	"github.com/Busnes-app/kyquickstart/internal/stack"
)

// mailPasswordFiles checks each identity.mail.password_file before preflight or apply
// connects. review does not, so a plan can be reviewed before the file exists.
func mailPasswordFiles(st *stack.Stack) error {
	for _, i := range st.Integrations {
		if i.Identity != nil && i.Identity.Mail != nil && i.Identity.Mail.PasswordFile != "" {
			pw, err := readMailPassword(i.Identity.Mail.PasswordFile)
			clear(pw)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// readMailPassword reads the workstation's SMTP password file: a regular 0600 file of this
// user, not a symlink, one line of at most 1024 bytes. The caller clears the result.
func readMailPassword(file string) ([]byte, error) {
	bad := func(why string) error {
		return fmt.Errorf("identity.mail.password_file %s %s. It must be a regular file of yours with mode 0600, not a symlink, holding one line of at most 1024 bytes", file, why)
	}
	// The checks are on the opened file. O_NONBLOCK: a FIFO is refused below, not waited on.
	f, err := os.OpenFile(file, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, bad("does not exist")
	case errors.Is(err, syscall.ELOOP):
		return nil, bad("is a symlink")
	case err != nil:
		return nil, bad(fmt.Sprintf("cannot be opened (%v)", errors.Unwrap(err)))
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, bad("cannot be read")
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	switch {
	case !fi.Mode().IsRegular():
		return nil, bad("is not a regular file")
	case !ok || int(st.Uid) != os.Getuid():
		return nil, bad("belongs to another user")
	case fi.Mode().Perm() != 0o600:
		return nil, bad(fmt.Sprintf("has mode %04o, not 0600 (chmod 600 %s)", fi.Mode().Perm(), file))
	}
	b := make([]byte, 1026)
	n, err := io.ReadFull(f, b)
	p := bytes.TrimSuffix(b[:n], []byte("\n"))
	why := ""
	switch {
	case err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF):
		why = "cannot be read"
	case len(p) == 0:
		why = "is empty"
	case len(p) > 1024:
		why = "is longer than 1024 bytes"
	case bytes.ContainsAny(p, "\r\n\x00"):
		why = "holds more than one line (or a CR or NUL byte)"
	}
	if why != "" {
		clear(b)
		return nil, bad(why)
	}
	return p, nil
}

// chatClientPlan refuses a chat_client: it was KyMessages' second KyIdentity client, and no
// catalog app takes one now.
func chatClientPlan(st *stack.Stack) error {
	for _, i := range st.Integrations {
		if i.ChatClient != "" {
			return fmt.Errorf("integration %s: chat_client was KyMessages' chat client, and no catalog app takes one; remove it", i.Product)
		}
	}
	return nil
}
