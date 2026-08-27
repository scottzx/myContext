package cli

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	mycontext "github.com/scottzx/mycontext"
	"github.com/scottzx/mycontext/internal/adapters/httpui"
	"github.com/scottzx/mycontext/internal/protocol"
)

func newUICmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "ui", Short: "Local web UI"}
	cmd.AddCommand(newUIServeCmd(opts))
	return cmd
}

func newUIServeCmd(opts *GlobalOptions) *cobra.Command {
	var port int
	var idleTimeout time.Duration
	var readOnly bool

	cmd := &cobra.Command{
		Use:         "serve",
		Short:       "Serve the read-only dashboard on 127.0.0.1 until Ctrl-C",
		Annotations: map[string]string{"op": "ui.serve"},
		Long: "Binds a random port on 127.0.0.1 only (never the network) and serves\n" +
			"the embedded static frontend plus one whitelisted invoke endpoint.\n" +
			"This is not a daemon: it runs only while this command runs, and stops\n" +
			"on Ctrl-C or after --idle-timeout with no requests.\n\n" +
			"Confirming an inbox item is only possible here: it needs a single-use\n" +
			"grant bound to this session, which no other entry point can issue.",
	}
	cmd.Flags().IntVar(&port, "port", 0, "fixed port (default: random)")
	cmd.Flags().DurationVar(&idleTimeout, "idle-timeout", 30*time.Minute, "stop after this long with no requests")
	cmd.Flags().BoolVar(&readOnly, "read-only", false, "serve without the capture and confirm operations")

	cmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "ui.serve"
		store, err := rt.OpsStore(readOnly)
		if err != nil {
			return rt.EmitError(command, err)
		}

		assets, err := fs.Sub(mycontext.WebDist, mycontext.WebDistDir)
		if err != nil {
			return rt.EmitError(command, protocol.Wrap(err, protocol.CodeInternal, "cannot open embedded web assets"))
		}

		server, err := httpui.New(store, assets, httpui.Options{
			Port:        port,
			IdleTimeout: idleTimeout,
			CLIVersion:  Version,
			Root:        rt.Root,
			Layout:      rt.Layout,
			Write:       !readOnly,
		})
		if err != nil {
			return rt.EmitError(command, err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
		go func() {
			<-sig
			fmt.Fprintln(os.Stderr, "\nstopping")
			cancel()
		}()

		if err := server.Serve(ctx); err != nil {
			return rt.EmitError(command, err)
		}
		return protocol.ExitOK
	})
	return cmd
}
