package runtime

import (
	"strings"

	"github.com/dongping/mateway/internal/model"
	"github.com/dongping/mateway/internal/tool"
)

type AcceptanceSpec struct {
	Ref                string
	CodeChecks         []string
	SoftFailureSignals []string
	PassCriteria       []string
	SuspectCriteria    []string
	FailCriteria       []string
	LLMReviewPrompt    string
}

type AcceptanceRegistry struct {
	specs map[string]AcceptanceSpec
}

func NewAcceptanceRegistry() *AcceptanceRegistry {
	r := &AcceptanceRegistry{specs: map[string]AcceptanceSpec{}}
	r.Register(AcceptanceSpec{
		Ref:                "project.index/default",
		CodeChecks:         []string{"output must not be empty", "evidence must include file path", "evidence should include project counts"},
		PassCriteria:       []string{"project path and file or directory counts are present"},
		SuspectCriteria:    []string{"project summary is present but structure evidence is weak"},
		FailCriteria:       []string{"missing project path evidence", "empty project summary"},
		LLMReviewPrompt:    "Review whether the project index output credibly summarizes the requested repository or directory structure, including path and meaningful structural counts.",
	})
	r.Register(AcceptanceSpec{
		Ref:                "skill.search/default",
		CodeChecks:         []string{"output must not be empty", "evidence should include query and result count"},
		SoftFailureSignals: []string{"no matching skills found"},
		PassCriteria:       []string{"skill search output includes result count and relevant skill information or explicit no-result evidence"},
		SuspectCriteria:    []string{"skill search output is present but relevance to the requested capability is weak"},
		FailCriteria:       []string{"missing skill search evidence", "empty skill search output"},
		LLMReviewPrompt:    "Review whether the skill search results are relevant to the requested capability and whether the output clearly communicates useful matches or an explicit no-result outcome.",
	})
	r.Register(AcceptanceSpec{
		Ref:                "skill.install/default",
		CodeChecks:         []string{"output must not be empty", "evidence should include skill name, target path, and install source"},
		SoftFailureSignals: []string{"skill name or url is required"},
		PassCriteria:       []string{"installed skill name, source, and target path are present"},
		SuspectCriteria:    []string{"skill install output is present but source or target context is incomplete"},
		FailCriteria:       []string{"missing skill install evidence", "empty install output"},
		LLMReviewPrompt:    "Review whether the skill installation result credibly shows which skill was installed, from where, and where it was placed in the workspace.",
	})
	r.Register(AcceptanceSpec{
		Ref:                "software.search/default",
		CodeChecks:         []string{"output must not be empty", "evidence should include query, provider, and result count"},
		SoftFailureSignals: []string{"no software results found"},
		PassCriteria:       []string{"search output includes concrete software results or explicit no-result evidence", "query/provider context is present"},
		SuspectCriteria:    []string{"software search returns weak or ambiguous results for the requested capability"},
		FailCriteria:       []string{"missing software search evidence", "empty output"},
		LLMReviewPrompt:    "Review whether the software search results are relevant to the requested software or installation need. Distinguish useful no-result evidence from weak or off-topic results.",
	})
	r.Register(AcceptanceSpec{
		Ref:                "memory.search/default",
		CodeChecks:         []string{"output must not be empty", "evidence must include file path", "evidence should include line range context"},
		SoftFailureSignals: []string{"no matching long memory found"},
		PassCriteria:       []string{"memory result includes path and line range", "snippet is relevant to the memory query"},
		SuspectCriteria:    []string{"memory result is present but line or snippet context is weak"},
		FailCriteria:       []string{"missing memory path evidence", "empty memory output"},
		LLMReviewPrompt:    "Review whether the memory search output actually matches the requested memory lookup and whether the snippet and line context are specific enough to trust.",
	})
	r.Register(AcceptanceSpec{
		Ref:                "memory.index/default",
		CodeChecks:         []string{"output must not be empty", "evidence must include file path", "evidence should include entry count"},
		SoftFailureSignals: []string{"memory root is required"},
		PassCriteria:       []string{"index path and entry count are present"},
		SuspectCriteria:    []string{"index summary is present but entry count or path evidence is weak"},
		FailCriteria:       []string{"missing index path evidence", "empty index output"},
		LLMReviewPrompt:    "Review whether the memory index output gives a credible summary of the current memory index state, especially the path and entry statistics.",
	})
	r.Register(AcceptanceSpec{
		Ref:                "file.read/default",
		CodeChecks:         []string{"output must not be empty", "evidence must include file path", "evidence should include line range context"},
		SoftFailureSignals: []string{"no such file", "is a directory", "path is required"},
		PassCriteria:       []string{"file content matches the requested file", "path and line range are present"},
		SuspectCriteria:    []string{"file content is present but path or line context is weak"},
		FailCriteria:       []string{"missing file path evidence", "empty file output"},
		LLMReviewPrompt:    "Review whether the file read output clearly corresponds to the requested file and includes enough concrete content or line context to support the step goal.",
	})
	r.Register(AcceptanceSpec{
		Ref:                "file.write/default",
		CodeChecks:         []string{"output must not be empty", "evidence must include target file path", "evidence should include bytes written"},
		SoftFailureSignals: []string{"path is required", "outside allowed roots"},
		PassCriteria:       []string{"target file path is present", "bytes written are recorded"},
		SuspectCriteria:    []string{"write succeeded but bytes evidence is weak or missing"},
		FailCriteria:       []string{"missing target file path evidence", "empty write output"},
		LLMReviewPrompt:    "Review whether the file write result clearly indicates the intended file was written and whether the output matches the step goal.",
	})
	r.Register(AcceptanceSpec{
		Ref:                "file.summary/default",
		CodeChecks:         []string{"output must not be empty", "evidence must include file path", "evidence should include preview or headings context"},
		SoftFailureSignals: []string{"requires a file path", "no such file", "is a directory"},
		PassCriteria:       []string{"summary reflects the requested file", "path/headings/preview are present"},
		SuspectCriteria:    []string{"summary is generic and does not clearly reflect the target file", "output lacks meaningful file content cues"},
		FailCriteria:       []string{"wrong file type", "missing path evidence", "empty summary"},
		LLMReviewPrompt:    "Review whether the file summary clearly matches the requested file and contains meaningful headings, preview, or metadata. Mark suspect when it reads like a generic summary rather than this file.",
	})
	r.Register(AcceptanceSpec{
		Ref:                "file.patch/default",
		CodeChecks:         []string{"output must not be empty", "evidence must include target file path", "patch output should mention the modified file"},
		SoftFailureSignals: []string{"old text not found", "old text is not unique"},
		PassCriteria:       []string{"patch result clearly indicates the intended file was modified", "diff summary is consistent with requested change"},
		SuspectCriteria:    []string{"patch succeeded but diff summary does not clearly reflect requested intent"},
		FailCriteria:       []string{"missing file path evidence", "patch output is empty"},
		LLMReviewPrompt:    "Review whether the patch result appears to satisfy the requested edit intent. Focus on whether the changed file and diff summary align with the step goal, not on exact formatting.",
	})
	r.Register(AcceptanceSpec{
		Ref:                "file.patch/append",
		CodeChecks:         []string{"output must not be empty", "evidence must include target file path", "patch output should mention the modified file"},
		SoftFailureSignals: []string{"old or append is required"},
		PassCriteria:       []string{"append result clearly indicates the intended file was updated", "diff summary is consistent with appended content"},
		SuspectCriteria:    []string{"append succeeded but output does not clearly show the appended target or effect"},
		FailCriteria:       []string{"missing file path evidence", "append output is empty"},
		LLMReviewPrompt:    "Review whether the append-style patch result clearly shows content was appended to the intended file and whether the result matches the step goal.",
	})
	r.Register(AcceptanceSpec{
		Ref:                "file.patch/replace",
		CodeChecks:         []string{"output must not be empty", "evidence must include target file path", "patch output should mention the modified file"},
		SoftFailureSignals: []string{"old text not found", "old text is not unique"},
		PassCriteria:       []string{"replace result clearly indicates the intended file was modified", "diff summary is consistent with requested replacement"},
		SuspectCriteria:    []string{"replace succeeded but diff summary does not clearly reflect replacement intent"},
		FailCriteria:       []string{"missing file path evidence", "replace output is empty"},
		LLMReviewPrompt:    "Review whether the replace-style patch result clearly reflects the requested textual replacement in the intended file.",
	})
	r.Register(AcceptanceSpec{
		Ref:                "web.search/default",
		CodeChecks:         []string{"output must not be empty", "evidence should include query, provider, and result count"},
		SoftFailureSignals: []string{"no results", "has no enabled provider"},
		PassCriteria:       []string{"search output includes concrete results or explicit no-result evidence", "query/provider context is present"},
		SuspectCriteria:    []string{"search output is present but weakly grounded or does not clearly answer the goal"},
		FailCriteria:       []string{"missing search execution evidence", "empty search output"},
		LLMReviewPrompt:    "Review whether the search output actually addresses the step goal. Distinguish between valid no-result outcomes with clear evidence and weak or irrelevant search output.",
	})
	r.Register(AcceptanceSpec{
		Ref:                "web.search/fresh_info",
		CodeChecks:         []string{"output must not be empty", "evidence should include query, provider, and result count"},
		SoftFailureSignals: []string{"no results", "has no enabled provider"},
		PassCriteria:       []string{"search output includes current or latest evidence", "query/provider context is present"},
		SuspectCriteria:    []string{"search output is present but freshness or relevance to latest information is weak"},
		FailCriteria:       []string{"missing search execution evidence", "empty search output"},
		LLMReviewPrompt:    "Review whether the search results actually support a latest/current/fresh answer. Prefer outputs with concrete recency cues and treat stale or weak freshness evidence as suspect.",
	})
	r.Register(AcceptanceSpec{
		Ref:                "web.search/background_info",
		CodeChecks:         []string{"output must not be empty", "evidence should include query, provider, and result count"},
		SoftFailureSignals: []string{"no results", "has no enabled provider"},
		PassCriteria:       []string{"search output includes useful background information", "query/provider context is present"},
		SuspectCriteria:    []string{"search output is present but does not clearly support background understanding"},
		FailCriteria:       []string{"missing search execution evidence", "empty search output"},
		LLMReviewPrompt:    "Review whether the search results provide useful background context for the step goal, even when the task is not freshness-sensitive.",
	})
	r.Register(AcceptanceSpec{
		Ref:                "terminal.run/diagnostic",
		CodeChecks:         []string{"output must not be empty", "evidence should include exit code, stdout, stderr, and timed_out"},
		SoftFailureSignals: []string{"not found", "data not found", "no results", "permission denied", "unauthorized", "timed out"},
		PassCriteria:       []string{"command output answers the diagnostic goal", "output and exit code are consistent"},
		SuspectCriteria:    []string{"exit code is zero but output indicates missing data or weak evidence", "output is present but does not clearly answer the goal"},
		FailCriteria:       []string{"non-zero exit code without acceptable explanation", "timed out before collecting evidence"},
		LLMReviewPrompt:    "Review whether the terminal command result actually answers the diagnostic goal. Treat zero-exit but weak or negative output as suspect when it does not meaningfully resolve the question.",
	})
	r.Register(AcceptanceSpec{
		Ref:                "terminal.run/build",
		CodeChecks:         []string{"output must not be empty", "evidence should include exit code, stdout, stderr, and timed_out"},
		SoftFailureSignals: []string{"build failed", "compilation failed", "timed out"},
		PassCriteria:       []string{"build command completed with consistent output and exit code", "output clearly indicates build success or actionable build failure"},
		SuspectCriteria:    []string{"build output is present but success or failure is ambiguous"},
		FailCriteria:       []string{"missing terminal execution evidence", "timed out before collecting build result"},
		LLMReviewPrompt:    "Review whether the build command output clearly indicates build success or an actionable build failure. Treat ambiguous build output as suspect.",
	})
	r.Register(AcceptanceSpec{
		Ref:                "terminal.run/test",
		CodeChecks:         []string{"output must not be empty", "evidence should include exit code, stdout, stderr, and timed_out"},
		SoftFailureSignals: []string{"test failed", "failing test", "timed out"},
		PassCriteria:       []string{"test command output clearly indicates pass or fail", "exit code and output are consistent"},
		SuspectCriteria:    []string{"test output is present but pass/fail state is ambiguous"},
		FailCriteria:       []string{"missing terminal execution evidence", "timed out before collecting test result"},
		LLMReviewPrompt:    "Review whether the test command output clearly indicates whether tests passed or failed, and whether the output is sufficient to support that conclusion.",
	})
	r.Register(AcceptanceSpec{
		Ref:                "software.install/default",
		CodeChecks:         []string{"output must not be empty", "evidence should include install command, verify command, and verified status"},
		SoftFailureSignals: []string{"install command is required", "not found", "permission denied", "timed out"},
		PassCriteria:       []string{"install command is explicit", "verify command is recorded", "verified is true when installation succeeds"},
		SuspectCriteria:    []string{"install appears to run but verification is weak or ambiguous"},
		FailCriteria:       []string{"missing install or verify evidence", "verification clearly failed"},
		LLMReviewPrompt:    "Review whether the installation result is credible: the install command should be explicit, verification evidence should be present, and the final status should match the tool goal.",
	})
	r.Register(AcceptanceSpec{
		Ref:                "schedule.create/default",
		CodeChecks:         []string{"output must not be empty", "evidence should include task id, status, schedule, and path"},
		SoftFailureSignals: []string{"missing_schedule_fields"},
		PassCriteria:       []string{"schedule task id, status, schedule, and path are present"},
		SuspectCriteria:    []string{"schedule create output is present but task summary is incomplete"},
		FailCriteria:       []string{"missing schedule task evidence", "empty schedule output"},
		LLMReviewPrompt:    "Review whether the schedule creation result clearly reflects the requested recurring task, including task id, schedule summary, and storage path.",
	})
	r.Register(AcceptanceSpec{
		Ref:                "schedule.list/default",
		CodeChecks:         []string{"output must not be empty", "evidence should include task count"},
		PassCriteria:       []string{"task count is present"},
		SuspectCriteria:    []string{"list output is present but count evidence is weak"},
		FailCriteria:       []string{"missing task count evidence", "empty schedule list output"},
		LLMReviewPrompt:    "Review whether the schedule list output gives a credible list or count summary for the user's request.",
	})
	r.Register(AcceptanceSpec{
		Ref:                "schedule.show/default",
		CodeChecks:         []string{"output must not be empty", "evidence should include task id, status, and path"},
		PassCriteria:       []string{"task id, status, and path are present"},
		SuspectCriteria:    []string{"show output is present but task context is incomplete"},
		FailCriteria:       []string{"missing schedule task evidence", "empty schedule show output"},
		LLMReviewPrompt:    "Review whether the schedule show output clearly identifies the requested task and its current state.",
	})
	r.Register(AcceptanceSpec{
		Ref:                "schedule.pause/default",
		CodeChecks:         []string{"output must not be empty", "evidence should include task id, status, and path"},
		PassCriteria:       []string{"paused task id and status are present"},
		SuspectCriteria:    []string{"pause output is present but resulting status is unclear"},
		FailCriteria:       []string{"missing schedule task evidence", "empty pause output"},
		LLMReviewPrompt:    "Review whether the schedule pause result clearly shows the intended task was paused.",
	})
	r.Register(AcceptanceSpec{
		Ref:                "schedule.resume/default",
		CodeChecks:         []string{"output must not be empty", "evidence should include task id, status, and path"},
		PassCriteria:       []string{"resumed task id and status are present"},
		SuspectCriteria:    []string{"resume output is present but resulting status is unclear"},
		FailCriteria:       []string{"missing schedule task evidence", "empty resume output"},
		LLMReviewPrompt:    "Review whether the schedule resume result clearly shows the intended task was resumed.",
	})
	r.Register(AcceptanceSpec{
		Ref:                "schedule.update/default",
		CodeChecks:         []string{"output must not be empty", "evidence should include task id, status, schedule, and path"},
		PassCriteria:       []string{"updated task id, status, schedule, and path are present"},
		SuspectCriteria:    []string{"update output is present but the resulting schedule context is incomplete"},
		FailCriteria:       []string{"missing updated schedule evidence", "empty update output"},
		LLMReviewPrompt:    "Review whether the schedule update result clearly reflects the requested schedule change.",
	})
	r.Register(AcceptanceSpec{
		Ref:                "schedule.delete/default",
		CodeChecks:         []string{"output must not be empty", "evidence should include task id and path"},
		PassCriteria:       []string{"deleted task id and path are present"},
		SuspectCriteria:    []string{"delete output is present but deletion context is incomplete"},
		FailCriteria:       []string{"missing delete evidence", "empty delete output"},
		LLMReviewPrompt:    "Review whether the schedule delete result clearly shows the intended task was deleted.",
	})
	return r
}

func (r *AcceptanceRegistry) Register(spec AcceptanceSpec) {
	if r == nil {
		return
	}
	if r.specs == nil {
		r.specs = map[string]AcceptanceSpec{}
	}
	r.specs[strings.TrimSpace(spec.Ref)] = spec
}

func (r *AcceptanceRegistry) Get(ref string) (AcceptanceSpec, bool) {
	if r == nil {
		return AcceptanceSpec{}, false
	}
	spec, ok := r.specs[strings.TrimSpace(ref)]
	return spec, ok
}

func acceptanceSpecForTool(reg *AcceptanceRegistry, def tool.Definition) (AcceptanceSpec, bool) {
	ref := strings.TrimSpace(def.Metadata.AcceptanceSpecRef)
	if ref == "" {
		return AcceptanceSpec{}, false
	}
	return reg.Get(ref)
}

func acceptanceSpecForStep(reg *AcceptanceRegistry, step model.PlanStep, def tool.Definition) (AcceptanceSpec, bool) {
	if reg == nil {
		return AcceptanceSpec{}, false
	}
	if ref := derivedAcceptanceSpecRef(step, def); strings.TrimSpace(ref) != "" {
		if spec, ok := reg.Get(ref); ok {
			return spec, true
		}
	}
	return acceptanceSpecForTool(reg, def)
}
