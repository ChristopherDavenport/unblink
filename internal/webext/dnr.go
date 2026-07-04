package webext

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"golang.org/x/net/publicsuffix"
)

// ResourceType is a declarativeNetRequest request category. unblink only ever issues
// script and xmlhttprequest subrequests (it never fetches passive images/CSS/fonts),
// but the full set is modeled so rules that enumerate other types parse correctly.
type ResourceType string

// declarativeNetRequest resource types.
const (
	TypeMainFrame  ResourceType = "main_frame"
	TypeSubFrame   ResourceType = "sub_frame"
	TypeStylesheet ResourceType = "stylesheet"
	TypeScript     ResourceType = "script"
	TypeImage      ResourceType = "image"
	TypeFont       ResourceType = "font"
	TypeObject     ResourceType = "object"
	TypeXHR        ResourceType = "xmlhttprequest"
	TypePing       ResourceType = "ping"
	TypeMedia      ResourceType = "media"
	TypeWebSocket  ResourceType = "websocket"
	TypeOther      ResourceType = "other"
)

// maxRegexLen caps a rule's regexFilter length; Go's regexp is RE2 (no catastrophic
// backtracking) but an absurdly long pattern is still refused as malformed input.
const maxRegexLen = 2000

// Request is a single network request evaluated against the rules.
type Request struct {
	URL       *url.URL
	Method    string       // matched case-insensitively
	Type      ResourceType // script / xmlhttprequest / other, per the issuing call site
	Initiator string       // the page's origin host (for initiatorDomains + first/third-party)
}

// Decision is the matcher's verdict for a request.
type Decision struct {
	Block      bool
	Allow      bool   // an allow/allowAllRequests rule out-prioritized any block
	RedirectTo string // non-empty for a redirect / upgradeScheme action
}

// DNRRule is one declarativeNetRequest rule, parsed but not yet compiled.
type DNRRule struct {
	ID        int
	Priority  int
	Action    DNRAction
	Condition DNRCondition
}

// DNRAction is a rule's action clause.
type DNRAction struct {
	Type        string // block|allow|allowAllRequests|redirect|upgradeScheme|modifyHeaders
	RedirectURL string // action.redirect.url (absolute)
}

// DNRCondition is a rule's condition clause.
type DNRCondition struct {
	URLFilter                string
	RegexFilter              string
	IsURLFilterCaseSensitive bool
	ResourceTypes            []ResourceType
	ExcludedResourceTypes    []ResourceType
	RequestDomains           []string
	ExcludedRequestDomains   []string
	InitiatorDomains         []string
	ExcludedInitiatorDomains []string
	RequestMethods           []string
	ExcludedRequestMethods   []string
	DomainType               string // "firstParty" | "thirdParty"
}

type rawRule struct {
	ID        int          `json:"id"`
	Priority  int          `json:"priority"`
	Action    rawAction2   `json:"action"`
	Condition rawCondition `json:"condition"`
}

type rawAction2 struct {
	Type     string `json:"type"`
	Redirect *struct {
		URL string `json:"url"`
	} `json:"redirect"`
}

type rawCondition struct {
	URLFilter                string   `json:"urlFilter"`
	RegexFilter              string   `json:"regexFilter"`
	IsURLFilterCaseSensitive bool     `json:"isUrlFilterCaseSensitive"`
	ResourceTypes            []string `json:"resourceTypes"`
	ExcludedResourceTypes    []string `json:"excludedResourceTypes"`
	RequestDomains           []string `json:"requestDomains"`
	ExcludedRequestDomains   []string `json:"excludedRequestDomains"`
	InitiatorDomains         []string `json:"initiatorDomains"`
	ExcludedInitiatorDomains []string `json:"excludedInitiatorDomains"`
	Domains                  []string `json:"domains"`         // legacy alias for initiatorDomains
	ExcludedDomains          []string `json:"excludedDomains"` // legacy alias
	RequestMethods           []string `json:"requestMethods"`
	ExcludedRequestMethods   []string `json:"excludedRequestMethods"`
	DomainType               string   `json:"domainType"`
}

// ParseRules parses a declarativeNetRequest ruleset file (a top-level JSON array of
// rule objects).
func ParseRules(data []byte) ([]DNRRule, error) {
	var raws []rawRule
	if err := json.Unmarshal(data, &raws); err != nil {
		return nil, fmt.Errorf("webext: parse ruleset: %w", err)
	}
	out := make([]DNRRule, 0, len(raws))
	for _, r := range raws {
		rule := DNRRule{
			ID:       r.ID,
			Priority: r.Priority,
			Action:   DNRAction{Type: r.Action.Type},
			Condition: DNRCondition{
				URLFilter:                r.Condition.URLFilter,
				RegexFilter:              r.Condition.RegexFilter,
				IsURLFilterCaseSensitive: r.Condition.IsURLFilterCaseSensitive,
				ResourceTypes:            toResourceTypes(r.Condition.ResourceTypes),
				ExcludedResourceTypes:    toResourceTypes(r.Condition.ExcludedResourceTypes),
				RequestDomains:           r.Condition.RequestDomains,
				ExcludedRequestDomains:   r.Condition.ExcludedRequestDomains,
				InitiatorDomains:         append(append([]string(nil), r.Condition.InitiatorDomains...), r.Condition.Domains...),
				ExcludedInitiatorDomains: append(append([]string(nil), r.Condition.ExcludedInitiatorDomains...), r.Condition.ExcludedDomains...),
				RequestMethods:           r.Condition.RequestMethods,
				ExcludedRequestMethods:   r.Condition.ExcludedRequestMethods,
				DomainType:               r.Condition.DomainType,
			},
		}
		if r.Action.Redirect != nil {
			rule.Action.RedirectURL = r.Action.Redirect.URL
		}
		out = append(out, rule)
	}
	return out, nil
}

func toResourceTypes(ss []string) []ResourceType {
	if len(ss) == 0 {
		return nil
	}
	out := make([]ResourceType, len(ss))
	for i, s := range ss {
		out[i] = ResourceType(strings.ToLower(s))
	}
	return out
}

// compiledRule is a rule prepared for fast matching.
type compiledRule struct {
	id       int
	priority int
	action   string
	redirect string

	types         map[ResourceType]bool
	excludedTypes map[ResourceType]bool
	reqDomains    []string
	exReqDomains  []string
	initDomains   []string
	exInitDomains []string
	methods       map[string]bool
	exMethods     map[string]bool
	domainType    string

	url *urlFilter     // nil when using regex or when there is no url condition
	re  *regexp.Regexp // nil unless regexFilter is set
}

func compileRule(r DNRRule) (*compiledRule, error) {
	prio := r.Priority
	if prio < 1 {
		prio = 1 // declarativeNetRequest priorities start at 1
	}
	action := r.Action.Type
	if action == "" {
		action = "block"
	}
	c := &compiledRule{
		id:            r.ID,
		priority:      prio,
		action:        action,
		redirect:      r.Action.RedirectURL,
		types:         typeSet(r.Condition.ResourceTypes),
		excludedTypes: typeSet(r.Condition.ExcludedResourceTypes),
		reqDomains:    lowerAll(r.Condition.RequestDomains),
		exReqDomains:  lowerAll(r.Condition.ExcludedRequestDomains),
		initDomains:   lowerAll(r.Condition.InitiatorDomains),
		exInitDomains: lowerAll(r.Condition.ExcludedInitiatorDomains),
		methods:       lowerSet(r.Condition.RequestMethods),
		exMethods:     lowerSet(r.Condition.ExcludedRequestMethods),
		domainType:    r.Condition.DomainType,
	}
	switch {
	case r.Condition.RegexFilter != "":
		if len(r.Condition.RegexFilter) > maxRegexLen {
			return nil, fmt.Errorf("webext: rule %d: regexFilter too long", r.ID)
		}
		pat := r.Condition.RegexFilter
		if !r.Condition.IsURLFilterCaseSensitive {
			pat = "(?i)" + pat
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, fmt.Errorf("webext: rule %d: bad regexFilter: %w", r.ID, err)
		}
		c.re = re
	case r.Condition.URLFilter != "":
		c.url = compileURLFilter(r.Condition.URLFilter, r.Condition.IsURLFilterCaseSensitive)
	}
	return c, nil
}

// matches reports whether the request satisfies every clause of the rule. rawURL and
// host are precomputed once per request by the matcher.
func (c *compiledRule) matches(req Request, rawURL, host string) bool {
	if len(c.types) > 0 && !c.types[req.Type] {
		return false
	}
	if c.excludedTypes[req.Type] {
		return false
	}
	if len(c.methods) > 0 && !c.methods[req.Method] {
		return false
	}
	if c.exMethods[req.Method] {
		return false
	}
	if len(c.reqDomains) > 0 && !domainListMatch(host, c.reqDomains) {
		return false
	}
	if len(c.exReqDomains) > 0 && domainListMatch(host, c.exReqDomains) {
		return false
	}
	if len(c.initDomains) > 0 && !domainListMatch(req.Initiator, c.initDomains) {
		return false
	}
	if len(c.exInitDomains) > 0 && domainListMatch(req.Initiator, c.exInitDomains) {
		return false
	}
	if c.domainType != "" {
		third := isThirdParty(host, req.Initiator)
		if (c.domainType == "thirdParty") != third {
			return false
		}
	}
	switch {
	case c.re != nil:
		return c.re.MatchString(rawURL)
	case c.url != nil:
		return c.url.match(rawURL)
	}
	return true
}

// RuleMatcher evaluates a request against a set of declarativeNetRequest rules. Static
// rules are immutable after construction (no lock); dynamic/session rules (added by
// extension JS in later phases) are guarded by mu. Match is safe for concurrent use.
type RuleMatcher struct {
	static []*compiledRule

	mu      sync.RWMutex
	dynamic []*compiledRule
	session []*compiledRule
}

// NewRuleMatcher compiles a slice of static rules into a matcher.
func NewRuleMatcher(rules []DNRRule) (*RuleMatcher, error) {
	m := &RuleMatcher{}
	for _, r := range rules {
		cr, err := compileRule(r)
		if err != nil {
			return nil, err
		}
		m.static = append(m.static, cr)
	}
	return m, nil
}

// Count reports the number of static rules (diagnostics/tests).
func (m *RuleMatcher) Count() int {
	if m == nil {
		return 0
	}
	return len(m.static)
}

// UpdateDynamic applies a declarativeNetRequest.updateDynamicRules change: remove the
// listed rule ids, then add the given rules. Safe for concurrent use with Match.
func (m *RuleMatcher) UpdateDynamic(add []DNRRule, removeIDs []int) error {
	return m.update(&m.dynamic, add, removeIDs)
}

// UpdateSession applies a declarativeNetRequest.updateSessionRules change.
func (m *RuleMatcher) UpdateSession(add []DNRRule, removeIDs []int) error {
	return m.update(&m.session, add, removeIDs)
}

func (m *RuleMatcher) update(set *[]*compiledRule, add []DNRRule, removeIDs []int) error {
	compiled := make([]*compiledRule, 0, len(add))
	for _, r := range add {
		cr, err := compileRule(r)
		if err != nil {
			return err
		}
		compiled = append(compiled, cr)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(removeIDs) > 0 {
		rm := make(map[int]bool, len(removeIDs))
		for _, id := range removeIDs {
			rm[id] = true
		}
		kept := (*set)[:0]
		for _, c := range *set {
			if !rm[c.id] {
				kept = append(kept, c)
			}
		}
		*set = kept
	}
	*set = append(*set, compiled...)
	return nil
}

// Match returns the highest-priority verdict for req. Among matching rules, an
// allow/allowAllRequests rule of priority ≥ the top blocking rule wins (Chrome's
// action-precedence model, simplified for the block/allow/redirect subset).
func (m *RuleMatcher) Match(req Request) Decision {
	if m == nil || req.URL == nil {
		return Decision{}
	}
	rawURL := req.URL.String()
	host := strings.ToLower(req.URL.Hostname())
	req.Method = strings.ToLower(req.Method)
	req.Initiator = strings.ToLower(req.Initiator)

	var block, allow *compiledRule
	consider := func(rules []*compiledRule) {
		for _, c := range rules {
			if !c.matches(req, rawURL, host) {
				continue
			}
			switch c.action {
			case "allow", "allowAllRequests":
				if allow == nil || c.priority > allow.priority {
					allow = c
				}
			case "block", "redirect", "upgradeScheme":
				if block == nil || c.priority > block.priority {
					block = c
				}
			}
		}
	}
	consider(m.static)
	m.mu.RLock()
	consider(m.dynamic)
	consider(m.session)
	m.mu.RUnlock()

	if allow != nil && (block == nil || allow.priority >= block.priority) {
		return Decision{Allow: true}
	}
	if block == nil {
		return Decision{}
	}
	switch block.action {
	case "redirect":
		return Decision{RedirectTo: block.redirect}
	case "upgradeScheme":
		return Decision{RedirectTo: strings.Replace(rawURL, "http://", "https://", 1)}
	default:
		return Decision{Block: true}
	}
}

func typeSet(ts []ResourceType) map[ResourceType]bool {
	if len(ts) == 0 {
		return nil
	}
	m := make(map[ResourceType]bool, len(ts))
	for _, t := range ts {
		m[t] = true
	}
	return m
}

func lowerSet(ss []string) map[string]bool {
	if len(ss) == 0 {
		return nil
	}
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[strings.ToLower(s)] = true
	}
	return m
}

func lowerAll(ss []string) []string {
	if len(ss) == 0 {
		return nil
	}
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = strings.ToLower(s)
	}
	return out
}

// domainListMatch reports whether host equals, or is a subdomain of, any domain.
func domainListMatch(host string, domains []string) bool {
	for _, d := range domains {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// isThirdParty compares the registrable domains of the request host and the page
// initiator. It falls back to a plain host comparison when the public-suffix lookup
// fails (e.g. an IP literal). An empty initiator is treated as first-party.
func isThirdParty(reqHost, initHost string) bool {
	if initHost == "" || reqHost == "" {
		return false
	}
	rd, err1 := publicsuffix.EffectiveTLDPlusOne(reqHost)
	id, err2 := publicsuffix.EffectiveTLDPlusOne(initHost)
	if err1 != nil || err2 != nil {
		return !strings.EqualFold(reqHost, initHost)
	}
	return !strings.EqualFold(rd, id)
}
