package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/axadrn/moin/internal/build"
	"github.com/axadrn/moin/internal/client"
)

// buckets lists, adds, changes and removes the workspace's buckets, and
// picks the bucket of an app. Each subcommand asks for the login before it
// looks at its arguments.
func buckets(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	command := "ls"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	switch command {
	case "ls":
		return bucketsList(ctx, args, stdout)
	case "add":
		return bucketsAdd(ctx, args, stdin, stdout)
	case "edit":
		return bucketsEdit(ctx, args, stdin, stdout)
	case "default":
		return bucketsDefault(ctx, args, stdout)
	case "rm":
		return bucketsRm(ctx, args, stdin, stdout)
	case "use":
		return bucketsUse(ctx, args, stdout)
	}
	return fmt.Errorf("unknown command %q: use %s buckets, buckets add, edit, default, rm or use", command, build.Name)
}

// bucketFlags are the settings of a bucket that add and edit take.
type bucketFlags struct {
	endpoint, region, bucket, folder, accessKey *string
	makeDefault                                 *bool
}

func newBucketFlags(flags *flagSet) bucketFlags {
	return bucketFlags{
		endpoint:    flags.String("endpoint", "", "https:// URL of an S3-compatible store; empty for AWS S3"),
		region:      flags.String("region", "", "region; auto for Cloudflare R2"),
		bucket:      flags.String("bucket", "", "the bucket's name at your bucket provider"),
		folder:      flags.String("path", "", "a folder inside the bucket, only if it holds other files too"),
		accessKey:   flags.String("access-key", "", "access key ID"),
		makeDefault: flags.Bool("default", false, "make it the default bucket of the workspace"),
	}
}

// bucketsList shows every bucket with the apps that use it.
func bucketsList(ctx context.Context, args []string, stdout io.Writer) error {
	flags := newFlags("buckets ls", "buckets")
	if err := flags.parse(args); err != nil {
		return err
	}
	api, err := apiClient()
	if err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return flags.wrong()
	}
	list, err := api.Buckets(ctx)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintf(stdout, "No bucket yet. Add one: %s buckets add default --endpoint https://… --bucket <bucket> --access-key <key>\n", build.Name)
		return nil
	}
	rows := [][]string{}
	for _, item := range list {
		name := item.Name
		if item.Default {
			name += " (default)"
		}
		apps := strings.Join(item.Apps, ", ")
		if len(item.OlderBackups) > 0 {
			apps = strings.TrimPrefix(apps+"; left by "+strings.Join(item.OlderBackups, ", "), "; ")
		}
		rows = append(rows, []string{name, item.Bucket, item.Folder, orAWS(item.Endpoint), item.AccessKey, dash(apps)})
	}
	table(stdout, []string{"NAME", "BUCKET", "FOLDER", "ENDPOINT", "ACCESS KEY", "APPS"}, rows)
	return nil
}

// bucketsAdd adds a bucket; the secret key comes on stdin.
func bucketsAdd(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	flags := newFlags("buckets add", "buckets add <name> --endpoint https://… --bucket <bucket> --access-key <key> [--region auto] [--path <folder>] [--default], then the secret key on stdin")
	settings := newBucketFlags(flags)
	if err := flags.parse(args); err != nil {
		return err
	}
	api, err := apiClient()
	if err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return flags.wrong()
	}
	if *settings.bucket == "" || *settings.accessKey == "" {
		return fmt.Errorf("give --bucket and --access-key; the secret key comes on stdin: %s", flags.usage)
	}
	secret, err := askSecret(stdin, stdout)
	if err != nil {
		return err
	}
	if secret == "" {
		return errors.New("the secret key is empty")
	}
	added, err := api.AddBucket(ctx, client.BucketInput{Name: flags.Arg(0), Endpoint: *settings.endpoint, Region: *settings.region, Bucket: *settings.bucket, Path: *settings.folder, AccessKey: *settings.accessKey, SecretKey: secret, Default: *settings.makeDefault})
	if err != nil {
		return err
	}
	if added.Default {
		done(stdout, "Added %s, the default bucket: uploads and backups of every app without a bucket of its own go there.", added.Name)
		return nil
	}
	done(stdout, "Added %s. Move an app there with %s buckets use %s <app>.", added.Name, build.Name, added.Name)
	return nil
}

// bucketsEdit changes the settings given as flags and keeps the others. A
// new access key, or --secret, reads a new secret key on stdin.
func bucketsEdit(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	flags := newFlags("buckets edit", "buckets edit <name> [--name <new name>] [--endpoint …] [--bucket …] [--path …] [--region …] [--access-key … or --secret, then the secret key on stdin]")
	settings := newBucketFlags(flags)
	rename := flags.String("name", "", "a new name in "+build.Name)
	newSecret := flags.Bool("secret", false, "read a new secret key on stdin")
	if err := flags.parse(args); err != nil {
		return err
	}
	api, err := apiClient()
	if err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return flags.wrong()
	}
	current, err := api.Bucket(ctx, flags.Arg(0))
	if err != nil {
		return err
	}
	input := current.Input()
	given := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { given[f.Name] = true })
	if given["endpoint"] {
		input.Endpoint = *settings.endpoint
	}
	if given["region"] {
		input.Region = *settings.region
	}
	if given["bucket"] {
		input.Bucket = *settings.bucket
	}
	if given["path"] {
		input.Path = *settings.folder
	}
	if given["access-key"] {
		input.AccessKey = *settings.accessKey
	}
	if given["name"] {
		input.Name = *rename
	}
	input.Default = current.Default || *settings.makeDefault
	if given["access-key"] || *newSecret {
		input.SecretKey, err = askSecret(stdin, stdout)
		if err != nil {
			return err
		}
		if input.SecretKey == "" {
			return errors.New("the secret key is empty")
		}
	}
	changed, err := api.EditBucket(ctx, current.Name, input)
	if err != nil {
		return err
	}
	done(stdout, "Saved %s.", changed.Name)
	return nil
}

// bucketsDefault makes a bucket the default of the workspace.
func bucketsDefault(ctx context.Context, args []string, stdout io.Writer) error {
	flags := newFlags("buckets default", "buckets default <name>")
	if err := flags.parse(args); err != nil {
		return err
	}
	api, err := apiClient()
	if err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return flags.wrong()
	}
	current, err := api.Bucket(ctx, flags.Arg(0))
	if err != nil {
		return err
	}
	input := current.Input()
	input.Default = true
	changed, err := api.EditBucket(ctx, current.Name, input)
	if err != nil {
		return err
	}
	done(stdout, "%s is the default bucket now. Apps without a bucket of their own upload and back up there; their history starts again there.", changed.Name)
	return nil
}

// bucketsRm removes a bucket from moin after asking for its name; what is
// in it stays.
func bucketsRm(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	flags := newFlags("buckets rm", "buckets rm <name> [--yes]").withYes()
	if err := flags.parse(args); err != nil {
		return err
	}
	api, err := apiClient()
	if err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return flags.wrong()
	}
	name := flags.Arg(0)
	err = confirmName(stdin, stdout, flags.yes(), "Remove the bucket "+name+" from "+build.Name+"? What is in it stays.", name)
	if err != nil {
		return err
	}
	err = api.RemoveBucket(ctx, name)
	if err != nil {
		return err
	}
	done(stdout, "Removed %s. What is in it stays.", name)
	return nil
}

// bucketsUse moves the uploads and new backups of an app to a bucket, or
// with --default back to the workspace's default bucket.
func bucketsUse(ctx context.Context, args []string, stdout io.Writer) error {
	flags := newFlags("buckets use", "buckets use <bucket> [app], or buckets use --default [app]").withApp()
	followDefault := flags.Bool("default", false, "follow the workspace's default bucket")
	if err := flags.parse(args); err != nil {
		return err
	}
	api, err := apiClient()
	if err != nil {
		return err
	}
	target, argument := "", ""
	switch {
	case *followDefault && flags.NArg() <= 1:
		argument = flags.Arg(0)
	case !*followDefault && flags.NArg() >= 1 && flags.NArg() <= 2:
		target, argument = flags.Arg(0), flags.Arg(1)
	default:
		return flags.wrong()
	}
	name, err := flags.app(argument)
	if err != nil {
		return err
	}
	app, err := api.SetAppBucket(ctx, name, target)
	if err != nil {
		return err
	}
	to := app.Bucket
	if to == "" {
		to = "the default bucket"
	}
	done(stdout, "Uploads and backups of %s go to %s now.", app.Name, to)
	fmt.Fprintf(stdout, "  The history starts again in the new bucket; what is in the old one stays there.\n")
	return nil
}

// askSecret reads a secret key from stdin, asking for it on a terminal.
func askSecret(stdin io.Reader, stdout io.Writer) (string, error) {
	if isTerminal(stdin) {
		fmt.Fprint(stdout, "Secret key: ")
	}
	secret, err := readSecret(stdin)
	if isTerminal(stdin) {
		fmt.Fprintln(stdout)
	}
	return secret, err
}

func orAWS(endpoint string) string {
	if endpoint == "" {
		return "AWS S3"
	}
	return endpoint
}
