package models

import "testing"

// TestMonitorTemplateValidate mirrors the rules of MonitorService.Validate: the
// API must not accept a blueprint that describes a monitor it would refuse to
// create (a certificate watch on a TCP template, a run_on=node without a node,
// a re-notification delay out of range).
func TestMonitorTemplateValidate(t *testing.T) {
	valid := func() *MonitorTemplate {
		return &MonitorTemplate{
			Name:     "blueprint",
			Type:     MonitorTypeHTTP,
			Config:   MonitorConfig{Method: "GET"},
			Defaults: TemplateDefaults{IntervalSeconds: 60, TimeoutSeconds: 10, RunOn: "all"},
		}
	}

	// A ssl template is an ordinary template: the type is a supported probe, and
	// the certificate watch is what it is for.
	ssl := valid()
	ssl.Type = MonitorTypeSSL
	ssl.Config = MonitorConfig{Host: "mail.example.com", Port: 993}
	ssl.Defaults.CertWatch = true
	ssl.Defaults.CertNotify = true
	ssl.Defaults.CertWarnDays = "30,7"
	if problem := ssl.Validate(); problem != "" {
		t.Fatalf("valid ssl template rejected: %s", problem)
	}

	// A tcp/dns template cannot watch a certificate: every monitor created from
	// it would be rejected by the monitor validation.
	for _, monitorType := range []MonitorType{MonitorTypeTCP, MonitorTypeDNS} {
		template := valid()
		template.Type = monitorType
		template.Defaults.CertWatch = true
		if problem := template.Validate(); problem == "" {
			t.Fatalf("cert_watch on a %s template must be rejected", monitorType)
		}
	}

	template := valid()
	template.Defaults.CertNotify = true
	if problem := template.Validate(); problem == "" {
		t.Fatal("cert_notify without cert_watch must be rejected")
	}

	template = valid()
	template.Defaults.DomainNotify = true
	if problem := template.Validate(); problem == "" {
		t.Fatal("domain_notify without domain_watch must be rejected")
	}

	template = valid()
	template.Defaults.RunOn = "node"
	if problem := template.Validate(); problem == "" {
		t.Fatal("run_on=node without node_id must be rejected")
	}
	template.Defaults.NodeID = "up-node-2"
	template.Defaults.RunOnNodes = "up-node-1"
	if problem := template.Validate(); problem != "" {
		t.Fatalf("valid run_on=node rejected: %s", problem)
	}
	if template.Defaults.RunOnNodes != "" {
		t.Fatalf("run_on_nodes must be cleared for run_on=node, got %q", template.Defaults.RunOnNodes)
	}

	template = valid()
	template.Defaults.Retries = 3
	template.Defaults.RetriesIntervalSeconds = 0
	if problem := template.Validate(); problem == "" {
		t.Fatal("a retry delay out of range must be rejected")
	}
	template.Defaults.RetriesIntervalSeconds = 15
	if problem := template.Validate(); problem != "" {
		t.Fatalf("valid retries rejected: %s", problem)
	}
}

// TestMonitorTemplateNormalize documents the defaults a template fills in: the
// values the UI always sends, plus the probe defaults of its type.
func TestMonitorTemplateNormalize(t *testing.T) {
	template := &MonitorTemplate{Name: "  blueprint  ", Type: MonitorTypeSSL, Config: MonitorConfig{Host: " example.com "}}
	template.Normalize()
	if template.Name != "blueprint" {
		t.Fatalf("name = %q", template.Name)
	}
	if template.Defaults.IntervalSeconds != 60 || template.Defaults.TimeoutSeconds != 10 {
		t.Fatalf("defaults = %+v", template.Defaults)
	}
	if template.Defaults.RetriesIntervalSeconds != 60 {
		t.Fatalf("retries_interval_seconds = %d, want 60", template.Defaults.RetriesIntervalSeconds)
	}
	if template.Defaults.RunOn != "all" {
		t.Fatalf("run_on = %q", template.Defaults.RunOn)
	}
	if template.Config.Host != "example.com" || template.Config.Port != DefaultSSLPort {
		t.Fatalf("config = %+v", template.Config)
	}
}

// TestMonitorTemplateNormalizeStripsAuth documents the rule that makes a monitor
// free to hold its own credentials: however a template is written (the API still
// accepts the fields for compatibility, an older UI sends them, a peer on an older
// release syncs them), what gets stored and handed back is credential free.
func TestMonitorTemplateNormalizeStripsAuth(t *testing.T) {
	template := &MonitorTemplate{
		Name: "blueprint",
		Type: MonitorTypeHTTP,
		Config: MonitorConfig{
			Method:      "POST",
			AuthType:    "basic",
			BasicUser:   "operator",
			BasicPass:   "hunter2",
			BearerToken: "token",
		},
	}
	template.Normalize()
	if template.Config.AuthType != "none" || template.Config.BasicUser != "" ||
		template.Config.BasicPass != "" || template.Config.BearerToken != "" {
		t.Fatalf("credentials survived Normalize: %+v", template.Config)
	}
	// The rest of the blueprint is untouched: a template still describes the probe.
	if template.Config.Method != "POST" || template.Config.Encoding != "json" {
		t.Fatalf("the probe options must be kept: %+v", template.Config)
	}
	// A credential free template is a valid one: nothing in the template rules
	// depends on the authentication any more.
	if problem := template.Validate(); problem != "" {
		t.Fatalf("a credential free template must be valid: %s", problem)
	}
}

// TestTemplateLinkScope covers the scope of a link run as a value: the default
// kind, the selection each kind requires, who has an outside and how an unset
// scope is told from a chosen one (the update path keeps the stored scope when the
// request carries none).
func TestTemplateLinkScope(t *testing.T) {
	// An unset scope is "nothing was decided": it normalizes to the type scope
	// and is recognisable before that, which is what the update path looks at.
	unset := TemplateLinkScope{}
	if !unset.IsZero() {
		t.Fatal("an empty scope must report IsZero")
	}
	// A template that was never linked is a valid template: the unset scope is
	// accepted and Normalize turns it into the type scope.
	if problem := unset.Valid(); problem != "" {
		t.Fatalf("an unset scope must be valid: %s", problem)
	}
	unset.Normalize()
	if unset.Kind != TemplateScopeType {
		t.Fatalf("kind = %q, want %q", unset.Kind, TemplateScopeType)
	}
	if unset.HasOutside() {
		t.Fatal("the type scope has no outside and must never detach")
	}
	if problem := unset.Valid(); problem != "" {
		t.Fatalf("the type scope must be valid: %s", problem)
	}

	// A scope that selects nothing is not a scope: the selection is required by
	// the kind, so a groups scope without groups is refused instead of detaching
	// every follower.
	for _, tc := range []struct {
		name  string
		scope TemplateLinkScope
	}{
		{name: "groups without groups", scope: TemplateLinkScope{Kind: TemplateScopeGroups}},
		{name: "tags without tags", scope: TemplateLinkScope{Kind: TemplateScopeTags}},
		{name: "unknown kind", scope: TemplateLinkScope{Kind: "everything"}},
	} {
		scope := tc.scope
		scope.Normalize()
		if problem := scope.Valid(); problem == "" {
			t.Errorf("%s must be rejected", tc.name)
		}
	}

	// Both selections have an outside, and normalize keeps only the one the kind
	// uses: a scope switched from groups to tags must not carry a stale group.
	groups := TemplateLinkScope{Kind: TemplateScopeGroups, GroupUUIDs: []string{"b", "a", "b"}, Tags: []string{"stale"}}
	groups.Normalize()
	if !groups.HasOutside() {
		t.Fatal("a group scope has an outside")
	}
	if len(groups.GroupUUIDs) != 2 || groups.GroupUUIDs[0] != "b" || groups.GroupUUIDs[1] != "a" {
		t.Fatalf("group_uuids = %v, want the duplicates dropped in order", groups.GroupUUIDs)
	}
	if len(groups.Tags) != 0 {
		t.Fatalf("a group scope kept the tags: %v", groups.Tags)
	}
	if problem := groups.Valid(); problem != "" {
		t.Fatalf("a group scope with groups must be valid: %s", problem)
	}
	tags := TemplateLinkScope{Kind: TemplateScopeTags, Tags: []string{"prod", "PROD"}}
	tags.Normalize()
	if !tags.HasOutside() {
		t.Fatal("a tag scope has an outside")
	}
	if len(tags.Tags) != 1 || tags.Tags[0] != "prod" {
		t.Fatalf("tags = %v, want the duplicate dropped", tags.Tags)
	}
}

// TestMonitorTemplateNormalizeFillsLinkScope keeps the two promises the link
// dialog makes about an existing template: it always answers with a known kind
// (never an empty one) and the scope survives a save that does not mention it.
func TestMonitorTemplateNormalizeFillsLinkScope(t *testing.T) {
	template := &MonitorTemplate{Name: "blueprint", Type: MonitorTypeHTTP, Config: MonitorConfig{Method: "GET"}}
	template.Normalize()
	if template.LinkScope.Kind != TemplateScopeType {
		t.Fatalf("link_scope.kind = %q, want %q", template.LinkScope.Kind, TemplateScopeType)
	}
	if problem := template.Validate(); problem != "" {
		t.Fatalf("the default scope must be valid: %s", problem)
	}
	template.LinkScope = TemplateLinkScope{Kind: TemplateScopeTags, Tags: []string{"prod"}}
	template.Normalize()
	if template.LinkScope.Kind != TemplateScopeTags || len(template.LinkScope.Tags) != 1 {
		t.Fatalf("a chosen scope must survive Normalize: %+v", template.LinkScope)
	}
	if problem := template.Validate(); problem != "" {
		t.Fatalf("a tag scope must be valid: %s", problem)
	}
}

// TestMonitorTypeTargetField keeps the bulk importer honest: the column a row
// fills is the target of the type, and ssl is a host:port probe like tcp.
func TestMonitorTypeTargetField(t *testing.T) {
	cases := map[MonitorType]string{
		MonitorTypeHTTP:    "url",
		MonitorTypeKeyword: "url",
		MonitorTypeTCP:     "host",
		MonitorTypeSSL:     "host",
		MonitorTypeDNS:     "hostname",
	}
	for monitorType, want := range cases {
		if got := monitorType.TargetField(); got != want {
			t.Errorf("%s target field = %q, want %q", monitorType, got, want)
		}
	}
}
