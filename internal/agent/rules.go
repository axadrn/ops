package agent

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/axadrn/moin/internal/build"
	"github.com/axadrn/moin/protocol"
)

// DefaultRulesFile is where the owner of a server writes what the agent
// may do there. MOIN_RULES overrides it. The agent never writes this file.
const DefaultRulesFile = "/etc/" + build.Name + "/rules"

// RulesFile returns MOIN_RULES, else DefaultRulesFile.
func RulesFile() string {
	if file := build.Getenv("RULES"); file != "" {
		return file
	}
	return DefaultRulesFile
}

// LoadRules reads the rules file. Without one, moin may do everything. A file
// with a mistake is an error, so a typo never allows what it meant to stop.
func LoadRules(file string) (*protocol.Rules, error) {
	body, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parseRules(string(body), file)
}

func parseRules(text, file string) (*protocol.Rules, error) {
	rules := &protocol.Rules{}
	for number, line := range strings.Split(text, "\n") {
		line, _, _ = strings.Cut(line, "#")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		wrong := func(want string) error {
			return fmt.Errorf("%s line %d: %q: %s", file, number+1, line, want)
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, wrong("write it as key: value")
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "data", "logs", "exec":
			if value != "on" && value != "off" {
				return nil, wrong(key + " is on or off")
			}
			switch key {
			case "data":
				rules.DataOff = value == "off"
			case "logs":
				rules.LogsOff = value == "off"
			default:
				rules.ExecOff = value == "off"
			}
		case "network":
			if value != "open" && value != "locked" {
				return nil, wrong("network is open or locked")
			}
			rules.NetworkLocked = value == "locked"
		case "deploy-from":
			for _, source := range strings.Split(value, ",") {
				source = strings.TrimSpace(source)
				if _, err := path.Match(source, ""); err != nil || source == "" {
					return nil, wrong("deploy-from lists sources like github.com/acme/*, ghcr.io/acme/* or upload")
				}
				rules.DeployFrom = append(rules.DeployFrom, source)
			}
		case "backups-to":
			host, bucket, ok := strings.Cut(value, "/")
			if !ok || host == "" || bucket == "" || strings.Contains(bucket, "/") {
				return nil, wrong("backups-to is <endpoint host>/<bucket>, such as s3.eu-central-1.amazonaws.com/acme-backups")
			}
			rules.BackupsTo = value
		default:
			return nil, wrong("unknown rule; the rules are data, logs, exec, deploy-from, backups-to and network")
		}
	}
	return rules, nil
}

// refused is the answer to a request the rules do not allow.
func refused(file, what string) error {
	return fmt.Errorf("refused by the rules of this server (%s): %s", file, what)
}

// checkDeploy allows a deploy whose source the rules allow. A redeploy or a
// rollback brings no new code and is always allowed.
func checkDeploy(rules *protocol.Rules, file string, start protocol.DeployStart) error {
	if rules == nil || len(rules.DeployFrom) == 0 {
		return nil
	}
	var sources []string
	if start.Image != "" {
		sources = append(sources, imageName(start.Image))
	}
	if start.SourceURL != "" {
		source, err := sourceName(start.SourceURL, rules.BackupsTo)
		if err != nil {
			return refused(file, err.Error())
		}
		sources = append(sources, source)
	}
	for _, source := range sources {
		if !allowed(rules.DeployFrom, source) {
			return refused(file, fmt.Sprintf("deploy-from does not list %s", source))
		}
	}
	return nil
}

func allowed(patterns []string, source string) bool {
	for _, pattern := range patterns {
		if ok, _ := path.Match(pattern, source); ok {
			return true
		}
	}
	return false
}

// imageName is an image reference without its tag or digest.
func imageName(image string) string {
	image, _, _ = strings.Cut(image, "@")
	slash := strings.LastIndex(image, "/")
	if colon := strings.LastIndex(image, ":"); colon > slash {
		image = image[:colon]
	}
	return image
}

// sourceName names where a source download comes from: a GitHub repo as
// github.com/<owner>/<repo>, anything else as upload. With backups-to set,
// an upload must come from that bucket.
func sourceName(raw, backupsTo string) (string, error) {
	link, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("the source address %q is not a URL", raw)
	}
	parts := strings.Split(strings.Trim(link.Path, "/"), "/")
	if len(parts) == 5 && parts[0] == "repos" && parts[3] == "tarball" {
		host := link.Host
		if host == "api.github.com" {
			host = "github.com"
		}
		return host + "/" + parts[1] + "/" + parts[2], nil
	}
	if backupsTo != "" && !inBucket(link, backupsTo) {
		return "", fmt.Errorf("an upload from %s is not in the bucket of backups-to", link.Host)
	}
	return "upload", nil
}

// inBucket reports whether a link points into "<endpoint host>/<bucket>",
// in path style or virtual-host style.
func inBucket(link *url.URL, hostBucket string) bool {
	host, bucket, _ := strings.Cut(hostBucket, "/")
	if link.Host == host {
		return strings.HasPrefix(link.Path, "/"+bucket+"/")
	}
	return link.Host == bucket+"."+host
}

// checkBucket allows a bucket the rules allow.
func checkBucket(rules *protocol.Rules, file string, bucket *protocol.Bucket) error {
	if rules == nil || rules.BackupsTo == "" || bucket == nil {
		return nil
	}
	host := "s3." + bucket.Region + ".amazonaws.com"
	if bucket.Endpoint != "" {
		endpoint, err := url.Parse(bucket.Endpoint)
		if err != nil {
			return refused(file, fmt.Sprintf("backups-to allows only %s", rules.BackupsTo))
		}
		host = endpoint.Host
	}
	if host+"/"+bucket.Bucket != rules.BackupsTo {
		return refused(file, fmt.Sprintf("backups-to allows only %s", rules.BackupsTo))
	}
	return nil
}
