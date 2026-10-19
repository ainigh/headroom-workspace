package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/ainigh/headroom-workspace/sysq/internal/history"
	"github.com/ainigh/headroom-workspace/sysq/internal/render"
	"github.com/ainigh/headroom-workspace/sysq/internal/setup"
)

func init() {
	extraCommands = append(extraCommands, func(root *cobra.Command) {
		root.AddCommand(historyCmd(), adminCmd())
	})
	// Scripts and recipes run from the CLI can read sysqd's history store.
	setupHooks = append(setupHooks, func(env *setup.Env) {
		db := filepath.Join(env.Dir, "history.db")
		if _, err := os.Stat(db); err != nil {
			return
		}
		st, err := history.Open(db, true)
		if err != nil {
			return
		}
		env.History = history.Register(env.Engine, st, nil)
	})
}

func historyCmd() *cobra.Command {
	var rng time.Duration
	var column, key string
	var events, narrative bool
	cmd := &cobra.Command{
		Use:   "history [recipe]",
		Short: "Read sysqd's history store: a recipe's series, events, or the narrative",
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := openEnv()
			if err != nil {
				return err
			}
			st, err := history.Open(filepath.Join(env.Dir, "history.db"), true)
			if err != nil {
				return fmt.Errorf("no history yet (sysqd records it): %w", err)
			}
			defer st.Close()
			ctx := context.Background()
			switch {
			case narrative:
				t, err := st.Narrative(ctx, rng, nil, nil)
				if err != nil {
					return err
				}
				fmt.Println(history.Text(t))
				return nil
			case events:
				t, err := st.Events(ctx, rng, "")
				if err != nil {
					return err
				}
				return render.Write(os.Stdout, flagFormat, []render.Item{{Table: t}})
			case len(args) == 0:
				names, err := st.Recipes(ctx)
				if err != nil {
					return err
				}
				for _, n := range names {
					fmt.Println(n)
				}
				return nil
			}
			t, err := st.Query(ctx, args[0], column, rng, key)
			if err != nil {
				return err
			}
			return render.Write(os.Stdout, flagFormat, []render.Item{{Label: args[0], Table: t}})
		},
	}
	cmd.Flags().DurationVar(&rng, "range", time.Hour, "how far back")
	cmd.Flags().StringVar(&column, "column", "", "one column")
	cmd.Flags().StringVar(&key, "key", "", "one key (e.g. a pid for proc.top.cpu)")
	cmd.Flags().BoolVar(&events, "events", false, "show event markers")
	cmd.Flags().BoolVar(&narrative, "narrative", false, "show the narrative text")
	return cmd
}

func adminClient(env *setup.Env) *http.Client {
	sock := filepath.Join(env.Dir, "admin.sock")
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
}

func adminCall(env *setup.Env, method, path string, body any) error {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, "http://admin"+path, r)
	resp, err := adminClient(env).Do(req)
	if err != nil {
		return fmt.Errorf("sysqd admin socket: %w (is sysqd running?)", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	fmt.Println(string(out))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s", resp.Status)
	}
	return nil
}

func adminCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "admin", Short: "Talk to sysqd's admin socket (pairing, clients, state)"}
	for _, c := range []struct{ use, short, method, path string }{
		{"status", "Daemon status", "GET", "/admin/status"},
		{"clients", "List paired API clients", "GET", "/admin/clients"},
		{"pending", "List pairing requests waiting for approval", "GET", "/admin/pending"},
		{"reload", "Re-read catalog, recipes, policy and config", "POST", "/admin/reload"},
	} {
		c := c
		cmd.AddCommand(&cobra.Command{Use: c.use, Short: c.short, RunE: func(*cobra.Command, []string) error {
			env, err := openEnv()
			if err != nil {
				return err
			}
			return adminCall(env, c.method, c.path, nil)
		}})
	}
	var privacy string
	var scripts bool
	approve := &cobra.Command{
		Use:   "approve <id> <code>",
		Short: "Approve a pairing request (privacy: aggregates, consumers, files)",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			env, err := openEnv()
			if err != nil {
				return err
			}
			return adminCall(env, "POST", "/admin/approve", map[string]any{"id": args[0], "code": args[1], "privacy": privacy, "allow_scripts": scripts})
		},
	}
	approve.Flags().StringVar(&privacy, "privacy", "aggregates", "aggregates | consumers | files")
	approve.Flags().BoolVar(&scripts, "allow-scripts", false, "let the client run sysq-script")
	revoke := &cobra.Command{
		Use:   "revoke <client-id>",
		Short: "Revoke a client's token",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			env, err := openEnv()
			if err != nil {
				return err
			}
			return adminCall(env, "DELETE", "/admin/clients/"+args[0], nil)
		},
	}
	cmd.AddCommand(approve, revoke)
	return cmd
}
