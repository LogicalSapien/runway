package poller

import (
	"context"
	"database/sql"
	"log"
	"os/exec"
	"time"

	dbpkg "github.com/LogicalSapien/runway/internal/db"
)

// GitPoller periodically runs git pull on all registered repos so that
// dispatched jobs always run against the latest commit.
type GitPoller struct {
	db       *sql.DB
	interval time.Duration
}

func NewGitPoller(db *sql.DB, _ string) *GitPoller {
	return &GitPoller{db: db, interval: 5 * time.Minute}
}

func (p *GitPoller) Run(ctx context.Context) {
	p.poll(ctx)
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.poll(ctx)
		}
	}
}

func (p *GitPoller) poll(ctx context.Context) {
	repos, err := dbpkg.ListRepos(p.db)
	if err != nil {
		log.Printf("git poller: list repos: %v", err)
		return
	}
	for _, r := range repos {
		if ctx.Err() != nil {
			return
		}
		if r.ClonePath == nil || *r.ClonePath == "" {
			continue
		}
		if err := gitPull(ctx, *r.ClonePath, r.DefaultBranch); err != nil {
			log.Printf("git poller: pull %s/%s: %v", r.Owner, r.Name, err)
		}
	}
}

func gitPull(ctx context.Context, dir, branch string) error {
	args := []string{"pull", "origin"}
	if branch != "" {
		args = append(args, "--", branch)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("git pull in %s: %v\n%s", dir, err, out)
	}
	return err
}
