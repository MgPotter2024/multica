package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var issueDeliverCmd = &cobra.Command{
	Use:   "deliver <id>",
	Short: "Deliver verified Agent work and move the issue to in_review",
	Args:  exactArgs(1),
	RunE:  runIssueDeliver,
}

type issueDeliveryCLIRequest struct {
	Content           string                            `json:"content"`
	ParentID          string                            `json:"parent_id,omitempty"`
	LocalVerification issueDeliveryCLILocalVerification `json:"local_verification"`
	CustomerPath      issueDeliveryCLICustomerPath      `json:"customer_path"`
}

type issueDeliveryCLILocalVerification struct {
	Command string `json:"command"`
	Result  string `json:"result"`
}

type issueDeliveryCLICustomerPath struct {
	Status   string `json:"status"`
	Method   string `json:"method,omitempty"`
	Surface  string `json:"surface,omitempty"`
	Evidence string `json:"evidence,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

func init() {
	issueDeliverCmd.Flags().String("content-file", "", "Read the result comment from a UTF-8 file inside the current working directory")
	issueDeliverCmd.Flags().String("parent", "", "Parent comment ID for comment-triggered tasks")
	issueDeliverCmd.Flags().String("local-command", "", "Local verification command that was run")
	issueDeliverCmd.Flags().String("local-result", "", "Bounded result summary from local verification")
	issueDeliverCmd.Flags().String("customer-path", "", "Customer-path status: passed or not_applicable")
	issueDeliverCmd.Flags().String("customer-method", "", "Customer-path method when passed: browser, cli, or api")
	issueDeliverCmd.Flags().String("customer-surface", "", "URL, command surface, or API path examined")
	issueDeliverCmd.Flags().String("customer-evidence", "", "Bounded observed customer-path result")
	issueDeliverCmd.Flags().String("customer-reason", "", "Reason no customer path applies")
	issueDeliverCmd.Flags().String("output", "json", "Output format: table or json")
}

func runIssueDeliver(cmd *cobra.Command, args []string) error {
	request, err := issueDeliveryRequestFromFlags(cmd)
	if err != nil {
		return err
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}

	var result map[string]any
	if err := client.PostJSON(ctx, "/api/issues/"+issueRef.ID+"/deliver", request, &result); err != nil {
		return fmt.Errorf("deliver issue: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	issue, _ := result["issue"].(map[string]any)
	fmt.Fprintf(os.Stderr, "Issue %s delivered with verification and moved to in_review.\n", issueDisplayKey(issue))
	return nil
}

func issueDeliveryRequestFromFlags(cmd *cobra.Command) (issueDeliveryCLIRequest, error) {
	contentFile, _ := cmd.Flags().GetString("content-file")
	if strings.TrimSpace(contentFile) == "" {
		return issueDeliveryCLIRequest{}, fmt.Errorf("--content-file is required")
	}
	inside, err := fileWithinWorkingDir(contentFile)
	if err != nil {
		return issueDeliveryCLIRequest{}, fmt.Errorf("validate --content-file: %w", err)
	}
	if !inside {
		return issueDeliveryCLIRequest{}, fmt.Errorf("--content-file path must be inside the current working directory")
	}
	contentBytes, err := os.ReadFile(contentFile)
	if err != nil {
		return issueDeliveryCLIRequest{}, fmt.Errorf("read --content-file: %w", err)
	}
	if !utf8.Valid(contentBytes) {
		return issueDeliveryCLIRequest{}, fmt.Errorf("--content-file must contain valid UTF-8")
	}
	content := string(contentBytes)
	if strings.TrimSpace(content) == "" {
		return issueDeliveryCLIRequest{}, fmt.Errorf("--content-file is empty")
	}

	localCommand, _ := cmd.Flags().GetString("local-command")
	localResult, _ := cmd.Flags().GetString("local-result")
	customerStatus, _ := cmd.Flags().GetString("customer-path")
	if strings.TrimSpace(localCommand) == "" || strings.TrimSpace(localResult) == "" {
		return issueDeliveryCLIRequest{}, fmt.Errorf("--local-command and --local-result are required")
	}
	if customerStatus != "passed" && customerStatus != "not_applicable" {
		return issueDeliveryCLIRequest{}, fmt.Errorf("--customer-path must be passed or not_applicable")
	}

	parent, _ := cmd.Flags().GetString("parent")
	request := issueDeliveryCLIRequest{
		Content: content, ParentID: parent,
		LocalVerification: issueDeliveryCLILocalVerification{Command: localCommand, Result: localResult},
		CustomerPath:      issueDeliveryCLICustomerPath{Status: customerStatus},
	}
	if customerStatus == "passed" {
		request.CustomerPath.Method, _ = cmd.Flags().GetString("customer-method")
		request.CustomerPath.Surface, _ = cmd.Flags().GetString("customer-surface")
		request.CustomerPath.Evidence, _ = cmd.Flags().GetString("customer-evidence")
		if strings.TrimSpace(request.CustomerPath.Method) == "" || strings.TrimSpace(request.CustomerPath.Surface) == "" || strings.TrimSpace(request.CustomerPath.Evidence) == "" {
			return issueDeliveryCLIRequest{}, fmt.Errorf("--customer-method, --customer-surface, and --customer-evidence are required when --customer-path=passed")
		}
	} else {
		request.CustomerPath.Reason, _ = cmd.Flags().GetString("customer-reason")
		if strings.TrimSpace(request.CustomerPath.Reason) == "" {
			return issueDeliveryCLIRequest{}, fmt.Errorf("--customer-reason is required when --customer-path=not_applicable")
		}
	}
	return request, nil
}
