package declared

import (
	"errors"
	"fmt"
	"net/http"

	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
)

func (c *compiler) calls(spec Calls) (callPlan, error) {
	var p callPlan
	var err error
	if p.formats, err = callFormats(spec.Formats); err != nil {
		return p, err
	}
	if !headerName(spec.ChargeRefHeader) {
		return p, fmt.Errorf("calls.charge_ref_header: %q is not a header name", spec.ChargeRefHeader)
	}
	p.chargeRef = http.CanonicalHeaderKey(spec.ChargeRefHeader)
	p.streams, p.cancels = spec.Streams, spec.Cancels
	if p.code, err = c.optionalPath("calls.errors.code", spec.Errors.Code); err != nil {
		return p, err
	}
	if p.resetAt, err = c.optionalPath("calls.errors.reset_at", spec.Errors.ResetAt); err != nil {
		return p, err
	}
	if p.rules, err = compileRules(spec.Errors.Rules, p.code); err != nil {
		return p, err
	}
	return p, nil
}

func callFormats(spec []string) ([]protocol.Format, error) {
	if len(spec) == 0 {
		return nil, errors.New("calls.formats: declare at least one format")
	}
	seen := map[protocol.Format]bool{}
	out := make([]protocol.Format, 0, len(spec))
	for _, name := range spec {
		f, ok := formatNames[name]
		if !ok {
			return nil, fmt.Errorf("calls.formats: %q is not a protocol format", name)
		}
		if seen[f] {
			return nil, fmt.Errorf("calls.formats: %q is listed twice", name)
		}
		seen[f] = true
		out = append(out, f)
	}
	return out, nil
}

func compileRules(spec []ErrorRule, code *path) ([]errorRule, error) {
	seen := map[[2]string]bool{}
	out := make([]errorRule, 0, len(spec))
	for i, r := range spec {
		rule, err := compileRule(r, code)
		if err != nil {
			return nil, fmt.Errorf("calls.errors.rules[%d]: %w", i, err)
		}
		key := [2]string{fmt.Sprint(rule.status), rule.code}
		if seen[key] {
			return nil, fmt.Errorf("calls.errors.rules[%d]: status %d with code %q is listed twice", i, rule.status, rule.code)
		}
		seen[key] = true
		out = append(out, rule)
	}
	return out, nil
}

func compileRule(r ErrorRule, code *path) (errorRule, error) {
	if r.Status != 0 && (r.Status < 400 || r.Status > 599) {
		return errorRule{}, fmt.Errorf("status %d is not a refusal status", r.Status)
	}
	if r.Code != "" && code == nil {
		return errorRule{}, errors.New("a code is named but calls.errors.code is not")
	}
	if r.Status == 0 && r.Code == "" {
		return errorRule{}, errors.New("a rule must name a status or a code")
	}
	cause, ok := causes[r.Cause]
	if !ok {
		return errorRule{}, fmt.Errorf("cause %q is not one this contract has", r.Cause)
	}
	scope, ok := scopes[r.Scope]
	if !ok {
		return errorRule{}, fmt.Errorf("scope %q is not one this contract has", r.Scope)
	}
	executed, err := ruleExecution(r)
	if err != nil {
		return errorRule{}, err
	}
	return errorRule{status: r.Status, code: r.Code, cause: cause, scope: scope, executed: executed}, nil
}

func ruleExecution(r ErrorRule) (adapter.Execution, error) {
	switch r.Executed {
	case "", "unknown":
		return adapter.ExecutionUnknown, nil
	case "no":
		if r.Status == 0 || r.Status >= http.StatusInternalServerError {
			return 0, errors.New("a 5xx, or a rule that could match one, may have run: it cannot say the request was not executed")
		}
		return adapter.NotExecuted, nil
	default:
		return 0, fmt.Errorf("executed %q, want no or unknown", r.Executed)
	}
}
