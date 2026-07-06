package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"text/tabwriter"
)

// defaultSkillsListLimit is how many skills `skills list` shows when --limit
// isn't given.
const defaultSkillsListLimit = 20

// runSkillsCommand implements `codeterminal-daemon skills <list|delete> ...`.
// There is deliberately no `skills add`: skills are captured by the agent
// loop in a later phase, not entered by hand from this CLI.
func runSkillsCommand(args []string, logger *log.Logger) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: skills <list|delete> ...")
	}

	switch args[0] {
	case "list":
		return runSkillsListCommand(args[1:], logger)
	case "delete":
		return runSkillsDeleteCommand(args[1:], logger)
	default:
		return fmt.Errorf("unknown skills subcommand %q (want list or delete)", args[0])
	}
}

// resolveSkillsDBPath returns override if set, otherwise the default
// per-user path.
func resolveSkillsDBPath(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	return DefaultSkillsDBPath()
}

// runSkillsListCommand implements
// `codeterminal-daemon skills list [--limit N] [--json] [--db path]`.
func runSkillsListCommand(args []string, logger *log.Logger) error {
	fset := flag.NewFlagSet("skills list", flag.ExitOnError)
	limit := fset.Int("limit", defaultSkillsListLimit, "maximum number of skills to show")
	asJSON := fset.Bool("json", false, "print as JSON instead of a table")
	dbPath := fset.String("db", "", "path to skills.db (default: ~/.codeterminal/skills.db)")
	fset.Parse(args)

	path, err := resolveSkillsDBPath(*dbPath)
	if err != nil {
		return err
	}

	store, err := OpenSkillStore(path)
	if err != nil {
		return err
	}
	defer store.Close()

	skills, err := store.ListSkills(context.Background(), *limit, 0)
	if err != nil {
		return err
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(skills)
	}

	printSkillsTable(skills)
	return nil
}

func printSkillsTable(skills []Skill) {
	if len(skills) == 0 {
		fmt.Println("no skills recorded")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tCREATED\tTITLE\tTAGS")
	for _, s := range skills {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.ID, s.CreatedAt.Format("2006-01-02 15:04:05"), s.Title, strings.Join(s.Tags, ","))
	}
	w.Flush()
}

// runSkillsDeleteCommand implements
// `codeterminal-daemon skills delete <id> [--db path]`.
func runSkillsDeleteCommand(args []string, logger *log.Logger) error {
	fset := flag.NewFlagSet("skills delete", flag.ExitOnError)
	dbPath := fset.String("db", "", "path to skills.db (default: ~/.codeterminal/skills.db)")
	fset.Parse(args)

	if fset.NArg() != 1 {
		return fmt.Errorf("usage: skills delete [--db path] <id>")
	}
	id := fset.Arg(0)

	path, err := resolveSkillsDBPath(*dbPath)
	if err != nil {
		return err
	}

	store, err := OpenSkillStore(path)
	if err != nil {
		return err
	}
	defer store.Close()

	if err := store.DeleteSkill(context.Background(), id); err != nil {
		return err
	}

	logger.Printf("skills: deleted %s", id)
	return nil
}
