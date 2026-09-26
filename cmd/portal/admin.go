package main

import (
	"context"
	"fmt"
	"os/user"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"better-api-portal/internal/auth"
	"better-api-portal/internal/store"
)

func adminCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Administer the portal's database directly",
	}
	var dsn string
	cmd.PersistentFlags().StringVar(&dsn, "dsn", "", "Postgres connection string (default $PORTAL_DSN)")
	open := func(ctx context.Context) (*store.Store, error) {
		d, err := dsnOrEnv(dsn)
		if err != nil {
			return nil, err
		}
		return store.Open(ctx, d)
	}
	cmd.AddCommand(tokenCmd(open))
	return cmd
}

// cliActor names whoever runs an admin command in the audit log.
func cliActor() string {
	if u, err := user.Current(); err == nil {
		return "cli:" + u.Username
	}
	return "cli"
}

func tokenCmd(open func(context.Context) (*store.Store, error)) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage static CI tokens, for CI systems without OIDC",
	}

	var repo string
	var days int
	create := &cobra.Command{
		Use:   "create",
		Short: "Issue a token that pushes for a repo",
		Long: `Issue a static token for a repo's CI, which sends it as
Authorization: Bearer. The token is printed once; only its hash is stored.

--repo is the repo's CI subject, the same value an ID token maps to (the
repoPrefix plus the repo claim), so a repo keeps its APIs when it moves to
OIDC.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if repo == "" {
				return fmt.Errorf("--repo is required")
			}
			if days < 1 {
				return fmt.Errorf("--expires-in must be at least 1 day")
			}
			ctx := cmd.Context()
			s, err := open(ctx)
			if err != nil {
				return err
			}
			defer s.Close()
			token, hash := auth.NewToken()
			expires := time.Now().Add(time.Duration(days) * 24 * time.Hour)
			id, err := s.CreateToken(ctx, repo, hash, expires, cliActor())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "token %d for %s, expires %s\n", id, repo, expires.Format(time.DateOnly))
			fmt.Fprintln(cmd.OutOrStdout(), token)
			return nil
		},
	}
	create.Flags().StringVar(&repo, "repo", "", "the repo's CI subject, e.g. acme/orders")
	create.Flags().IntVar(&days, "expires-in", 90, "days until the token expires")

	list := &cobra.Command{
		Use:   "list",
		Short: "List tokens, including expired and revoked ones",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			s, err := open(ctx)
			if err != nil {
				return err
			}
			defer s.Close()
			tokens, err := s.Tokens(ctx)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tREPO\tSTATE\tEXPIRES\tLAST USED\tCREATED BY")
			for _, t := range tokens {
				state := "live"
				switch {
				case t.RevokedAt != nil:
					state = "revoked"
				case time.Now().After(t.ExpiresAt):
					state = "expired"
				}
				used := "never"
				if t.LastUsedAt != nil {
					used = t.LastUsedAt.Format(time.DateTime)
				}
				fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", t.ID, t.Repo, state,
					t.ExpiresAt.Format(time.DateOnly), used, t.CreatedBy)
			}
			return w.Flush()
		},
	}

	revoke := &cobra.Command{
		Use:   "revoke ID",
		Short: "Revoke a token",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("token id %q is not a number", args[0])
			}
			ctx := cmd.Context()
			s, err := open(ctx)
			if err != nil {
				return err
			}
			defer s.Close()
			return s.RevokeToken(ctx, id, cliActor())
		},
	}
	cmd.AddCommand(create, list, revoke)
	return cmd
}
