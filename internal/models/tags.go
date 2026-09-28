package models

import "strings"

// MaxTagsLength mirrors the column size of Monitor.Tags. A tag list longer than
// this is refused by the write paths instead of reaching the driver (MySQL in
// strict mode answers an error, and the API would report it as a 500).
const MaxTagsLength = 255

// Tags are the free form, comma separated list of a monitor (Monitor.Tags). They
// are deliberately NOT a join table: the list is small, it travels as one field
// on the wire and the operator types it in one box.
//
// The helpers below are what makes a tag usable as a *selection* (a bulk tag
// operation, the scope of a template link run) instead of a decoration:
//
//   - NormalizeTags / JoinTags keep the stored form deterministic (trimmed,
//     de-duplicated case insensitively, order preserved) so two monitors tagged
//     "Ops" and "ops" are the same tag and a monitor never carries "prod, prod".
//   - HasAnyTag compares one tag at a time and exactly. The `?tag=` filter of the
//     monitors list is a substring match (`LIKE %term%`, see
//     services.MonitorService.List), which is fine for a search box but wrong for
//     a selection: it would silently pull "production" into a scope meant for
//     "prod".
//   - JoinTags is deliberately NOT models.JoinList: that helper sorts its items,
//     and reordering the tags of a monitor on every write hides the edit and
//     rewrites rows for nothing (the notification templates keep their own
//     order-preserving form for the same reason).
func NormalizeTags(raw string) []string {
	fields := SplitList(raw)
	out := make([]string, 0, len(fields))
	seen := make(map[string]bool, len(fields))
	for _, field := range fields {
		tag := strings.TrimSpace(field)
		if tag == "" {
			continue
		}
		key := strings.ToLower(tag)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, tag)
	}
	return out
}

// JoinTags renders a tag list as the stored comma separated string, preserving
// the order of the first spelling of every tag.
func JoinTags(tags []string) string {
	return strings.Join(NormalizeTags(strings.Join(tags, ",")), ",")
}

// CleanTags normalizes a stored tag list: the canonical form every write path
// stores ("prod, prod" becomes "prod", surrounding spaces disappear, the
// spelling of the first occurrence wins).
func CleanTags(raw string) string {
	return JoinTags(NormalizeTags(raw))
}

// AddTags appends the missing tags to a stored list, keeping the existing order.
func AddTags(current string, add []string) string {
	existing := NormalizeTags(current)
	return JoinTags(append(existing, add...))
}

// RemoveTags drops the listed tags (case insensitive) from a stored list.
func RemoveTags(current string, remove []string) string {
	dropped := make(map[string]bool, len(remove))
	for _, tag := range NormalizeTags(strings.Join(remove, ",")) {
		dropped[strings.ToLower(tag)] = true
	}
	kept := make([]string, 0, len(remove))
	for _, tag := range NormalizeTags(current) {
		if !dropped[strings.ToLower(tag)] {
			kept = append(kept, tag)
		}
	}
	return JoinTags(kept)
}

// HasAnyTag reports whether a stored tag list carries at least one of the wanted
// tags: the comparison is exact (whole tag) and case insensitive.
//
// An empty `wanted` list never matches: an empty selection is not "everything",
// which is the same rule the group scope follows.
func HasAnyTag(current string, wanted []string) bool {
	if len(wanted) == 0 {
		return false
	}
	have := make(map[string]bool)
	for _, tag := range NormalizeTags(current) {
		have[strings.ToLower(tag)] = true
	}
	for _, tag := range NormalizeTags(strings.Join(wanted, ",")) {
		if have[strings.ToLower(tag)] {
			return true
		}
	}
	return false
}
