package models

import (
	"fmt"
	"strconv"
)

// ---------------------------------------------------------------------------
// Catalogue of the hardware alert rules
// ---------------------------------------------------------------------------
//
// A rule is described once, here, and every layer reads the description instead
// of repeating it: the API serves AllSystemAlertSpecs() to the browser (whose
// form is built from the ranges below), NormalizeSystemAlert clamps to them, and
// the notifications format their values with the unit of the metric.
//
// Adding a rule is therefore a new entry in systemAlertSpecs plus the column (or
// the count query) it evaluates, and nothing else has to be remembered.

// SystemAlertUnit is the display unit of a rule. "%", "°C" and "MB/s" are
// symbols the UI appends to the number; "load", "containers" and "disks" are
// words it shows in the help of the rule instead.
type SystemAlertUnit string

const (
	SystemAlertUnitPercent    SystemAlertUnit = "%"
	SystemAlertUnitLoad       SystemAlertUnit = "load"
	SystemAlertUnitCelsius    SystemAlertUnit = "°C"
	SystemAlertUnitBandwidth  SystemAlertUnit = "MB/s"
	SystemAlertUnitContainers SystemAlertUnit = "containers"
	SystemAlertUnitDisks      SystemAlertUnit = "disks"
)

// SystemAlertSpec describes one entry of the catalogue.
type SystemAlertSpec struct {
	Metric SystemAlertMetric `json:"metric"`
	Unit   SystemAlertUnit   `json:"unit"`
	// Min, Max and Step are the domain of the threshold (the number input of the
	// dialog): a value outside it falls back to DefaultThreshold.
	Min  float64 `json:"min"`
	Max  float64 `json:"max"`
	Step float64 `json:"step"`
	// HasThreshold is false for the rules that watch an event instead of a
	// number (`down`): their threshold is not editable.
	HasThreshold bool `json:"has_threshold"`
	// HasWindow is false for the state rules (`container`, `smart`): they fire
	// as soon as the count reaches the threshold and average nothing, so they
	// have no sustained minutes.
	HasWindow bool `json:"has_window"`
	// DefaultEnabled is what a new machine starts with.
	DefaultEnabled bool `json:"default_enabled"`
	// DefaultThreshold and DefaultMinutes are the built-in defaults: the values
	// "restore the built-in values" puts back in Admin > Settings.
	DefaultThreshold float64 `json:"default_threshold"`
	DefaultMinutes   int     `json:"default_minutes"`
}

// systemAlertSpecs is the catalogue, in the order the dialog shows it.
//
// The numbers are Beszel's: 80% of CPU, memory, swap and GPU, 85% of the root
// filesystem, 10 of load, 80°C, 80 MB/s, one unhealthy container and one failing
// disk, all sustained five minutes (the state rules fire immediately).
var systemAlertSpecs = []SystemAlertSpec{
	{Metric: SystemAlertDown, Unit: "", Min: 0, Max: 0, Step: 0, HasThreshold: false, HasWindow: true, DefaultEnabled: true, DefaultThreshold: 0, DefaultMinutes: 5},
	{Metric: SystemAlertCPU, Unit: SystemAlertUnitPercent, Min: 1, Max: 100, Step: 1, HasThreshold: true, HasWindow: true, DefaultEnabled: true, DefaultThreshold: 80, DefaultMinutes: 5},
	{Metric: SystemAlertMemory, Unit: SystemAlertUnitPercent, Min: 1, Max: 100, Step: 1, HasThreshold: true, HasWindow: true, DefaultEnabled: true, DefaultThreshold: 80, DefaultMinutes: 5},
	{Metric: SystemAlertSwap, Unit: SystemAlertUnitPercent, Min: 1, Max: 100, Step: 1, HasThreshold: true, HasWindow: true, DefaultEnabled: true, DefaultThreshold: 80, DefaultMinutes: 5},
	{Metric: SystemAlertDisk, Unit: SystemAlertUnitPercent, Min: 1, Max: 100, Step: 1, HasThreshold: true, HasWindow: true, DefaultEnabled: true, DefaultThreshold: 85, DefaultMinutes: 5},
	{Metric: SystemAlertLoad, Unit: SystemAlertUnitLoad, Min: 0, Max: 64, Step: 0.1, HasThreshold: true, HasWindow: true, DefaultEnabled: true, DefaultThreshold: 10, DefaultMinutes: 5},
	{Metric: SystemAlertLoad5, Unit: SystemAlertUnitLoad, Min: 0, Max: 64, Step: 0.1, HasThreshold: true, HasWindow: true, DefaultEnabled: true, DefaultThreshold: 10, DefaultMinutes: 5},
	{Metric: SystemAlertLoad15, Unit: SystemAlertUnitLoad, Min: 0, Max: 64, Step: 0.1, HasThreshold: true, HasWindow: true, DefaultEnabled: true, DefaultThreshold: 10, DefaultMinutes: 5},
	{Metric: SystemAlertTemperature, Unit: SystemAlertUnitCelsius, Min: 40, Max: 100, Step: 1, HasThreshold: true, HasWindow: true, DefaultEnabled: true, DefaultThreshold: 80, DefaultMinutes: 5},
	{Metric: SystemAlertBandwidth, Unit: SystemAlertUnitBandwidth, Min: 1, Max: 250, Step: 1, HasThreshold: true, HasWindow: true, DefaultEnabled: true, DefaultThreshold: 80, DefaultMinutes: 5},
	{Metric: SystemAlertGPU, Unit: SystemAlertUnitPercent, Min: 1, Max: 100, Step: 1, HasThreshold: true, HasWindow: true, DefaultEnabled: true, DefaultThreshold: 80, DefaultMinutes: 5},
	{Metric: SystemAlertContainer, Unit: SystemAlertUnitContainers, Min: 1, Max: 100, Step: 1, HasThreshold: true, HasWindow: false, DefaultEnabled: true, DefaultThreshold: 1, DefaultMinutes: 0},
	{Metric: SystemAlertSmart, Unit: SystemAlertUnitDisks, Min: 1, Max: 100, Step: 1, HasThreshold: true, HasWindow: false, DefaultEnabled: true, DefaultThreshold: 1, DefaultMinutes: 0},
}

// AllSystemAlertSpecs returns a copy of the catalogue (the caller may sort it).
func AllSystemAlertSpecs() []SystemAlertSpec {
	out := make([]SystemAlertSpec, len(systemAlertSpecs))
	copy(out, systemAlertSpecs)
	return out
}

// SystemAlertSpecOf returns the description of one rule.
func SystemAlertSpecOf(metric SystemAlertMetric) (SystemAlertSpec, bool) {
	for _, spec := range systemAlertSpecs {
		if spec.Metric == metric {
			return spec, true
		}
	}
	return SystemAlertSpec{}, false
}

// SystemAlertUnitOf returns the unit of a rule ("" for an unknown metric).
func SystemAlertUnitOf(metric SystemAlertMetric) SystemAlertUnit {
	spec, ok := SystemAlertSpecOf(metric)
	if !ok {
		return ""
	}
	return spec.Unit
}

// FormatSystemAlertValue renders a measured value with its unit: "82.4%",
// "12.35 load", "85.0°C", "12.3 MB/s". The notification detail is built with it
// so a bandwidth alert does not read "average 12.3%".
func FormatSystemAlertValue(metric SystemAlertMetric, value float64) string {
	switch SystemAlertUnitOf(metric) {
	case SystemAlertUnitPercent:
		return fmt.Sprintf("%.1f%%", value)
	case SystemAlertUnitCelsius:
		return fmt.Sprintf("%.1f°C", value)
	case SystemAlertUnitBandwidth:
		return fmt.Sprintf("%.1f MB/s", value)
	case SystemAlertUnitLoad:
		return fmt.Sprintf("%.2f load", value)
	default:
		return formatAlertNumber(value)
	}
}

// FormatSystemAlertThreshold renders a threshold the way the same rule reads in
// the form: "80%", "38.5°C", "80 MB/s", "10", "1".
func FormatSystemAlertThreshold(metric SystemAlertMetric, value float64) string {
	suffix := ""
	switch SystemAlertUnitOf(metric) {
	case SystemAlertUnitPercent:
		suffix = "%"
	case SystemAlertUnitCelsius:
		suffix = "°C"
	case SystemAlertUnitBandwidth:
		suffix = " MB/s"
	}
	return formatAlertNumber(value) + suffix
}

// SystemAlertCountDetail is the detail line of a state rule: "2 unhealthy
// containers (threshold 1)", "1 failing disks (threshold 1)".
func SystemAlertCountDetail(metric SystemAlertMetric, count int64, threshold float64) string {
	label := "items"
	switch metric {
	case SystemAlertContainer:
		label = "unhealthy containers"
	case SystemAlertSmart:
		label = "failing disks"
	}
	return fmt.Sprintf("%d %s (threshold %s)", count, label, FormatSystemAlertThreshold(metric, threshold))
}

// formatAlertNumber prints a threshold without trailing zeros ("80", "38.5").
func formatAlertNumber(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}
