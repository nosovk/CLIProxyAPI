package test

import (
	"os"
	"strings"
	"testing"
)

func TestForkSyncWorkflowUsesTargetCommitForImageMetadata(t *testing.T) {
	workflowBytes, errRead := os.ReadFile("../.github/workflows/fork-sync-build.yml")
	if errRead != nil {
		t.Fatalf("read fork sync workflow: %v", errRead)
	}
	workflow := string(workflowBytes)

	required := []string{
		"commit_short_sha: ${{ steps.get_sha.outputs.commit_short_sha }}",
		`echo "commit_short_sha=$(git rev-parse --short=7 HEAD)" >> $GITHUB_OUTPUT`,
		"type=raw,value=${{ env.TARGET_BRANCH }}",
		"type=raw,value=${{ needs.sync-and-test.outputs.commit_short_sha }}",
		"org.opencontainers.image.revision=${{ needs.sync-and-test.outputs.commit_sha }}",
		"org.opencontainers.image.version=${{ env.TARGET_BRANCH }}",
	}
	for _, snippet := range required {
		if !strings.Contains(workflow, snippet) {
			t.Errorf("workflow missing target metadata contract %q", snippet)
		}
	}

	forbidden := []string{
		"type=sha,format=short,prefix=",
		"type=ref,event=branch",
	}
	for _, snippet := range forbidden {
		if strings.Contains(workflow, snippet) {
			t.Errorf("workflow still derives image metadata from workflow context %q", snippet)
		}
	}
}
