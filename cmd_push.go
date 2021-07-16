package main

import (
	"context"
	"fmt"

	"github.com/skeema/mybase"
	"github.com/skeema/skeema/internal/applier"
	"github.com/skeema/skeema/internal/fs"
	"github.com/skeema/skeema/internal/linter"
	"github.com/skeema/skeema/internal/workspace"
	"golang.org/x/sync/errgroup"
)

func init() {
	summary := "Alter objects on DBs to reflect the filesystem representation"
	desc := "Modifies the schemas on database instance(s) to match the corresponding " +
		"filesystem representation of them. This essentially performs the same diff logic " +
		"as `skeema diff`, but then actually runs the generated DDL. You should generally " +
		"run `skeema diff` first to see what changes will be applied.\n\n" +
		"You may optionally pass an environment name as a CLI arg. This will affect " +
		"which section of .skeema config files is used for processing. For example, " +
		"running `skeema push staging` will apply config directives from the " +
		"[staging] section of config files, as well as any sectionless directives at the " +
		"top of the file. If no environment name is supplied, the default is \"production\".\n\n" +
		"An exit code of 0 will be returned if the operation was fully successful; 1 if " +
		"at least one table could not be updated due to use of unsupported features, or if " +
		"the --dry-run option was used and differences were found; or 2+ if a fatal error " +
		"occurred."

	cmd := mybase.NewCommand("push", summary, desc, PushHandler)

	cmd.AddOptions("DDL generation",
		mybase.BoolOption("exact-match", 0, false, "Follow *.sql table definitions exactly, even for differences with no functional impact"),
		mybase.BoolOption("compare-metadata", 0, false, "For stored programs, detect changes to creation-time sql_mode or DB collation"),
		mybase.BoolOption("alter-validate-virtual", 0, false, "Apply a WITH VALIDATION clause to ALTER TABLEs affecting virtual columns"),
		mybase.StringOption("alter-lock", 0, "", `Apply a LOCK clause to all ALTER TABLEs (valid values: "none", "shared", "exclusive")`),
		mybase.StringOption("alter-algorithm", 0, "", `Apply an ALGORITHM clause to all ALTER TABLEs (valid values: "inplace", "copy", "instant")`),
		mybase.StringOption("partitioning", 0, "keep", `Specify handling of partitioning status on the database side (valid values: "keep", "remove", "modify")`),
	)

	cmd.AddOptions("External tool",
		mybase.StringOption("alter-wrapper", 'x', "", "External bin to shell out to for ALTER TABLE; see manual for template vars"),
		mybase.StringOption("alter-wrapper-min-size", 0, "0", "Ignore --alter-wrapper for tables smaller than this size in bytes"),
		mybase.StringOption("ddl-wrapper", 'X', "", "Like --alter-wrapper, but applies to all DDL types (CREATE, DROP, ALTER)"),
	)

	cmd.AddOptions("linter rule",
		mybase.BoolOption("lint", 0, true, "Check modified objects for problems before proceeding"),
	)
	linter.AddCommandOptions(cmd)

	cmd.AddOptions("safety",
		mybase.BoolOption("verify", 0, true, "Test all generated ALTER statements on temp schema to verify correctness"),
		mybase.BoolOption("allow-unsafe", 0, false, "Permit running ALTER or DROP operations that are potentially destructive"),
		mybase.BoolOption("dry-run", 0, false, "Output DDL but don't run it; equivalent to `skeema diff`"),
		mybase.BoolOption("foreign-key-checks", 0, false, "Force the server to check referential integrity of any new foreign key"),
		mybase.StringOption("safe-below-size", 0, "0", "Always permit destructive operations for tables below this size in bytes"),
	)

	cmd.AddOptions("sharding",
		mybase.BoolOption("first-only", '1', false, "For dirs mapping to multiple instances or schemas, just run against the first per dir"),
		mybase.BoolOption("brief", 'q', false, "<overridden by diff command>").Hidden(),
		mybase.StringOption("concurrent-instances", 'c', "1", "Perform operations on this number of instances concurrently"),
	)

	workspace.AddCommandOptions(cmd)
	cmd.AddArg("environment", "production", false)
	CommandSuite.AddSubCommand(cmd)
	clonePushOptionsToDiff()
}

// PushHandler is the handler method for `skeema push`
func PushHandler(cfg *mybase.Config) error {
	dir, err := fs.ParseDir(".", cfg)
	if err != nil {
		return err
	}

	briefMode := dir.Config.GetBool("dry-run") && dir.Config.GetBool("brief")
	printer := applier.NewPrinter(briefMode)
	g, ctx := errgroup.WithContext(context.Background())
	tgchan, skipCount := applier.TargetGroupChanForDir(dir)
	results := make(chan applier.Result)

	workerCount, err := dir.Config.GetInt("concurrent-instances")
	if err == nil && workerCount < 1 {
		err = fmt.Errorf("concurrent-instances cannot be less than 1")
	}
	if err != nil {
		return NewExitValue(CodeBadConfig, err.Error())
	}
	for n := 0; n < workerCount; n++ {
		g.Go(func() error {
			return applier.Worker(ctx, tgchan, results, printer)
		})
	}
	go func() {
		g.Wait()
		close(results)
	}()

	allResults := make([]applier.Result, 0, workerCount)
	for r := range results {
		allResults = append(allResults, r)
	}
	if err := g.Wait(); err != nil {
		if _, ok := err.(applier.ConfigError); ok {
			return NewExitValue(CodeBadConfig, err.Error())
		}
		return err
	}
	sum := applier.SumResults(allResults)
	sum.SkipCount += skipCount

	if sum.SkipCount+sum.UnsupportedCount == 0 {
		if dir.Config.GetBool("dry-run") && sum.Differences {
			return NewExitValue(CodeDifferencesFound, "")
		}
		return nil
	}
	code := CodeFatalError
	if sum.SkipCount == 0 {
		code = CodePartialError
	}
	return NewExitValue(code, sum.Summary())
}
