package k8s

import (
	"context"
	"fmt"
	"strings"
	"time"

	cmdutil "github.com/canonical/k8sd/cmd/util"
	"github.com/canonical/k8sd/pkg/client/k8sd"
	"github.com/spf13/cobra"
)

const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorCyan   = "\033[36m"
	colorBold   = "\033[1m"
	colorDim    = "\033[2m"
)

type UpgradeCheckResult struct {
	FromChannel string                         `json:"from_channel" yaml:"from_channel"`
	ToChannel   string                         `json:"to_channel" yaml:"to_channel"`
	Verdict     string                         `json:"verdict" yaml:"verdict"`
	Components  []k8sd.UpgradeComponentResult  `json:"components" yaml:"components"`
	Summary     string                         `json:"summary" yaml:"summary"`
}

func verdictBadge(v string) string {
	switch v {
	case "pass":
		return "PASS"
	case "warn":
		return "WARN"
	case "blocked":
		return "BLOCKED"
	default:
		return strings.ToUpper(v)
	}
}

func colorVerdict(v string) string {
	badge := verdictBadge(v)
	switch badge {
	case "PASS":
		return colorGreen + badge + colorReset
	case "WARN":
		return colorYellow + badge + colorReset
	case "BLOCKED":
		return colorRed + badge + colorReset
	default:
		return badge
	}
}

func (r UpgradeCheckResult) String() string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("%sUpgrade check:%s %s → %s  [%s]\n",
		colorBold, colorReset,
		colorCyan+r.FromChannel+colorReset,
		colorCyan+r.ToChannel+colorReset,
		colorVerdict(r.Verdict)))

	maxNameLen := len("Component")
	maxFromLen := len("From")
	maxToLen := len("To")
	maxVerdictLen := len("Verdict")
	for _, c := range r.Components {
		if len(c.Name) > maxNameLen {
			maxNameLen = len(c.Name)
		}
		if len(c.FromVersion) > maxFromLen {
			maxFromLen = len(c.FromVersion)
		}
		if len(c.ToVersion) > maxToLen {
			maxToLen = len(c.ToVersion)
		}
		if v := verdictBadge(c.Verdict); len(v) > maxVerdictLen {
			maxVerdictLen = len(v)
		}
	}

	nameFmt := fmt.Sprintf("%%-%ds", maxNameLen)
	fromFmt := fmt.Sprintf("%%-%ds", maxFromLen)
	toFmt := fmt.Sprintf("%%-%ds", maxToLen)
	verdictFmt := fmt.Sprintf("%%-%ds", maxVerdictLen)

	divider := fmt.Sprintf("%s+%s+%s+%s+%s+%s\n",
		colorDim,
		strings.Repeat("-", maxNameLen+2),
		strings.Repeat("-", maxFromLen+2),
		strings.Repeat("-", maxToLen+2),
		strings.Repeat("-", maxVerdictLen+2),
		colorReset)

	sb.WriteString(divider)
	sb.WriteString(fmt.Sprintf("%s| "+nameFmt+" | "+fromFmt+" | "+toFmt+" | "+verdictFmt+" |%s\n",
		colorBold, "Component", "From", "To", "Verdict", colorReset))
	sb.WriteString(divider)

	for _, c := range r.Components {
		verdictStr := colorVerdict(c.Verdict)
		padding := maxVerdictLen - len(verdictBadge(c.Verdict))
		sb.WriteString(fmt.Sprintf("| "+nameFmt+" | "+fromFmt+" | "+toFmt+" | %s%s |\n",
			c.Name, c.FromVersion, c.ToVersion, verdictStr, strings.Repeat(" ", padding)))
	}
	sb.WriteString(divider)

	for _, c := range r.Components {
		for _, w := range c.Warnings {
			severity := strings.ToUpper(w.Severity)
			var severityColor string
			switch severity {
			case "HIGH", "CRITICAL":
				severityColor = colorRed
			case "MEDIUM":
				severityColor = colorYellow
			default:
				severityColor = colorDim
			}
			sb.WriteString(fmt.Sprintf("\n  [%s%s%s] %s%s%s: %s\n",
				severityColor, severity, colorReset,
				colorBold, c.Name, colorReset,
				w.Message))
		}
	}

	if r.Summary != "" {
		sb.WriteString(fmt.Sprintf("\n%s\n", r.Summary))
	}

	return sb.String()
}

func newUpgradeCheckCmd(env cmdutil.ExecutionEnvironment) *cobra.Command {
	var opts struct {
		fromChannel  string
		toChannel    string
		outputFormat string
		timeout      time.Duration
	}
	cmd := &cobra.Command{
		Use:    "upgrade-check",
		Short:  "Check upgrade compatibility between snap channels",
		Long:   "Check if the current Kubernetes cluster can be safely upgraded to the target snap channel.",
		Args:   cobra.NoArgs,
		PreRun: chainPreRunHooks(hookRequireRoot(env), hookInitializeFormatter(env, &opts.outputFormat)),
		Run: func(cmd *cobra.Command, args []string) {
			if opts.timeout < minTimeout {
				cmd.PrintErrf("Timeout %v is less than minimum of %v. Using the minimum %v instead.\n", opts.timeout, minTimeout, minTimeout)
				opts.timeout = minTimeout
			}

			if opts.toChannel == "" {
				cmd.PrintErrln("Error: --to-channel must not be empty.")
				env.Exit(1)
				return
			}

			client, err := env.Snap.K8sdClient("")
			if err != nil {
				cmd.PrintErrf("Error: Failed to create a k8sd client. Make sure that the k8sd service is running.\n\nThe error was: %v\n", err)
				env.Exit(1)
				return
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), opts.timeout)
			cobra.OnFinalize(cancel)

			cmd.PrintErrf("Downloading and analyzing snap contents, this may take a moment...\n")

			request := k8sd.UpgradeCheckRequest{
				FromChannel: opts.fromChannel,
				ToChannel:   opts.toChannel,
			}

			response, err := client.UpgradeCheck(ctx, request)
			if err != nil {
				cmd.PrintErrf("Error: Failed to check upgrade from %q to %q.\n\nThe error was: %v\n", opts.fromChannel, opts.toChannel, err)
				env.Exit(1)
				return
			}

			outputFormatter.Print(UpgradeCheckResult{
				FromChannel: response.FromChannel,
				ToChannel:   response.ToChannel,
				Verdict:     response.Verdict,
				Components:  response.Components,
				Summary:     response.Summary,
			})
		},
	}

	cmd.Flags().StringVar(&opts.fromChannel, "from-channel", "", "the snap channel to compare from (default: current snap channel)")
	cmd.Flags().StringVar(&opts.toChannel, "to-channel", "", "the snap channel to upgrade to")
	cmd.Flags().StringVar(&opts.outputFormat, "output-format", "plain", "set the output format to one of plain, json or yaml")
	cmd.Flags().DurationVar(&opts.timeout, "timeout", 90*time.Second, "the max time to wait for the command to execute")

	_ = cmd.MarkFlagRequired("to-channel")

	return cmd
}