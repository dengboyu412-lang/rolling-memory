// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package cli implements the rolling-memory / rmem command line, its option
// table, and the human/JSON renderers.
//
// The package owns argv parsing, exit codes (0 success, 1 processed
// application error, 2 usage error) and output formatting. Business decisions
// stay in internal/memory/facade and the surface packages wired in here (mcp, hooks,
// harness); the CLI adds no policy of its own.
package cli

import (
	"fmt"
	"strconv"
	"strings"
)

// optionKind is the CLI parser action/type pair used by one option.
type optionKind int

const (
	kindString optionKind = iota
	kindInt
	kindFlagTrue
	kindFlagFalse
)

// Option is one long option in the parser table (CLI parser add_argument).
type Option struct {
	Names    []string
	Dest     string
	Help     string
	Kind     optionKind
	Choices  []string
	Default  any
	Required bool
	Metavar  string
}

func (o *Option) name() string { return o.Names[0] }

func (o *Option) isFlag() bool { return o.Kind == kindFlagTrue || o.Kind == kindFlagFalse }

// usage renders the option the way CLI parser folds it into a usage line.
func (o *Option) usage() string {
	body := o.name()
	if !o.isFlag() {
		body += " " + o.metavar()
	}
	if o.Required {
		return body
	}
	return "[" + body + "]"
}

func (o *Option) metavar() string {
	if o.Metavar != "" {
		return o.Metavar
	}
	if len(o.Choices) > 0 {
		return "{" + strings.Join(o.Choices, ",") + "}"
	}
	// CLI parser's default metavar is dest.upper(); underscores stay.
	return strings.ToUpper(o.Dest)
}

// label renders the option the way CLI parser lists it under "options:": every
// option string, plus the metavar for options that take a value.
func (o *Option) label() string {
	names := strings.Join(o.Names, ", ")
	if o.isFlag() {
		return names
	}
	return names + " " + o.metavar()
}

// positional is one positional argument (nargs default, "?" or "*").
type positional struct {
	Name     string
	Help     string
	Choices  []string
	Required bool
	Variadic bool
}

func (p *positional) usage() string {
	body := p.Name
	if len(p.Choices) > 0 {
		body = "{" + strings.Join(p.Choices, ",") + "}"
	}
	if p.Variadic {
		return "[" + body + " ...]"
	}
	if p.Required {
		return body
	}
	return "[" + body + "]"
}

// label renders the positional the way CLI parser lists it: the choices set when
// it has one (mode's name, hook's event), otherwise the bare name.
func (p *positional) label() string {
	if len(p.Choices) > 0 {
		return "{" + strings.Join(p.Choices, ",") + "}"
	}
	return p.Name
}

// command is one node of the subcommand tree.
type command struct {
	Name        string
	Path        []string
	Help        string
	Description string
	Options     []*Option
	Positionals []*positional
	Subcommands []*command
	SubDest     string
	// DefaultSub 是未显式给出子命令时落入的动作（eval 裸调用 = 全流程 "all"）；
	// 为空时缺少子命令仍按用法错误处理。
	DefaultSub string
}

// helpOption is CLI parser's implicit -h/--help; it takes part in the
// long-option abbreviation search so "--hel" resolves to help.
var helpOption = &Option{Names: []string{"--help"}, Dest: helpDest, Kind: kindFlagTrue,
	Help: "show this help message and exit"}

// helpDest marks the implicit help action (never stored in Args).
const helpDest = "__help__"

// longOption is one option string of a command, in CLI parser registration
// order (help first, then the table, aliases in place).
type longOption struct {
	option *Option
	name   string
}

func (c *command) longOptions() []longOption {
	result := make([]longOption, 0, len(c.Options)+1)
	result = append(result, longOption{helpOption, "--help"})
	for _, option := range c.Options {
		for _, name := range option.Names {
			result = append(result, longOption{option, name})
		}
	}
	return result
}

func (c *command) findOption(name string) *Option {
	for _, option := range c.Options {
		for _, candidate := range option.Names {
			if candidate == name {
				return option
			}
		}
	}
	return nil
}

// matchOption resolves one long option the way CLI parser does: an exact name
// wins, otherwise a unique prefix does (allow_abbrev; CLI parser
// _get_option_tuples). candidates lists every match, in table order, when the
// prefix is ambiguous.
func (c *command) matchOption(name string) (option *Option, matched string, candidates []string) {
	if found := c.findOption(name); found != nil {
		return found, name, nil
	}
	matches := []longOption{}
	for _, candidate := range c.longOptions() {
		if strings.HasPrefix(candidate.name, name) {
			matches = append(matches, candidate)
		}
	}
	if len(matches) == 1 {
		return matches[0].option, matches[0].name, nil
	}
	for _, candidate := range matches {
		candidates = append(candidates, candidate.name)
	}
	return nil, "", candidates
}

func (c *command) findSubcommand(name string) *command {
	for _, sub := range c.Subcommands {
		if sub.Name == name {
			return sub
		}
	}
	return nil
}

func (c *command) subNames() []string {
	names := make([]string, 0, len(c.Subcommands))
	for _, sub := range c.Subcommands {
		names = append(names, sub.Name)
	}
	return names
}

// Parser is the table-driven replacement for CLI parser.
type Parser struct {
	Prog        string
	Description string
	root        *command
}

// UsageError is an CLI parser-style usage failure; Main exits 2.
type UsageError struct {
	Usage   string
	Message string
}

func (e *UsageError) Error() string { return e.Message }

// HelpRequest reports a -h/--help hit; Main prints Text and exits 0.
type HelpRequest struct{ Text string }

func (e *HelpRequest) Error() string { return "help requested" }

// Args is one parsed command line.
type Args struct {
	path     []string
	values   map[string]any
	provided map[string]bool
	version  bool
}

// Command returns the first-level subcommand.
func (a *Args) Command() string {
	if len(a.path) == 0 {
		return ""
	}
	return a.path[0]
}

// Subcommand returns the second-level subcommand (config/govern/sync).
func (a *Args) Subcommand() string {
	if len(a.path) < 2 {
		return ""
	}
	return a.path[1]
}

// String returns a string value, or "" when the option is absent.
func (a *Args) String(name string) string {
	value, _ := a.values[name].(string)
	return value
}

// StringPtr returns nil when the option is absent, mirroring None.
func (a *Args) StringPtr(name string) *string {
	if !a.provided[name] {
		return nil
	}
	value, ok := a.values[name].(string)
	if !ok {
		return nil
	}
	return &value
}

// LookupString distinguishes an absent option from an empty one.
func (a *Args) LookupString(name string) (string, bool) {
	if !a.provided[name] {
		return "", false
	}
	value, ok := a.values[name].(string)
	return value, ok
}

// Int returns an int value, or 0 when the option is absent.
func (a *Args) Int(name string) int {
	value, _ := a.values[name].(int)
	return value
}

// Float returns a float value, or 0 when the option is absent.
func (a *Args) Float(name string) float64 {
	switch value := a.values[name].(type) {
	case float64:
		return value
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err == nil {
			return parsed
		}
	}
	return 0
}

// IntPtr returns nil when the option is absent.
func (a *Args) IntPtr(name string) *int {
	if !a.provided[name] {
		return nil
	}
	value, ok := a.values[name].(int)
	if !ok {
		return nil
	}
	return &value
}

// Flag returns a boolean value, or false when the option is absent.
func (a *Args) Flag(name string) bool {
	value, _ := a.values[name].(bool)
	return value
}

// FlagPtr keeps the CLI parser None default of tri-state flags.
func (a *Args) FlagPtr(name string) *bool {
	if !a.provided[name] {
		return nil
	}
	value, ok := a.values[name].(bool)
	if !ok {
		return nil
	}
	return &value
}

// List returns a variadic positional slice.
func (a *Args) List(name string) []string {
	value, _ := a.values[name].([]string)
	return value
}

// Parse resolves argv against the table. Errors are *UsageError or
// *HelpRequest; both are printed by Main and are not application failures.
func (p *Parser) Parse(argv []string) (*Args, error) {
	args := &Args{values: map[string]any{}, provided: map[string]bool{}}
	if err := p.parseLevel(p.root, argv, args); err != nil {
		return nil, err
	}
	return args, nil
}

func (p *Parser) parseLevel(cmd *command, tokens []string, args *Args) error {
	args.path = cmd.Path
	for _, option := range cmd.Options {
		if option.Default != nil && !args.provided[option.Dest] {
			args.values[option.Dest] = option.Default
		}
	}
	for _, pos := range cmd.Positionals {
		if pos.Variadic {
			args.values[pos.Name] = []string{}
		}
	}

	positionalIndex := 0
	stopOptions := false
	var selected *command
	var remainder []string

	for index := 0; index < len(tokens); index++ {
		token := tokens[index]
		if !stopOptions && token == "--" {
			stopOptions = true
			continue
		}
		if !stopOptions && (token == "-h" || token == "--help") {
			return &HelpRequest{Text: p.help(cmd)}
		}
		if !stopOptions && strings.HasPrefix(token, "--") {
			name, value, attached := strings.Cut(token, "=")
			option, matched, candidates := cmd.matchOption(name)
			if len(candidates) > 1 {
				return p.usageError(cmd, "ambiguous option: %s could match %s",
					name, strings.Join(candidates, ", "))
			}
			if option == nil {
				return p.usageError(cmd, "unrecognized arguments: %s", token)
			}
			if option.Dest == helpDest {
				if attached {
					return p.usageError(cmd, "argument %s: ignored explicit argument '%s'", matched, value)
				}
				return &HelpRequest{Text: p.help(cmd)}
			}
			if option.Dest == "__version__" {
				args.version = true
				return nil
			}
			if option.isFlag() {
				if attached {
					return p.usageError(cmd, "argument %s: ignored explicit argument '%s'", matched, value)
				}
				if option.Kind == kindFlagTrue {
					args.values[option.Dest] = true
				} else {
					args.values[option.Dest] = false
				}
				args.provided[option.Dest] = true
				continue
			}
			if !attached {
				if index+1 >= len(tokens) || looksLikeOption(tokens[index+1]) {
					return p.usageError(cmd, "argument %s: expected one argument", matched)
				}
				index++
				value = tokens[index]
			}
			if err := p.assignOption(cmd, option, matched, value, args); err != nil {
				return err
			}
			continue
		}
		if !stopOptions && token != "-" && strings.HasPrefix(token, "-") {
			return p.usageError(cmd, "unrecognized arguments: %s", token)
		}

		if len(cmd.Subcommands) > 0 {
			sub := cmd.findSubcommand(token)
			if sub == nil {
				return p.usageError(cmd, "argument %s: invalid choice: '%s' (choose from %s)",
					cmd.SubDest, token, quoteChoices(cmd.subNames()))
			}
			selected = sub
			remainder = tokens[index+1:]
			break
		}
		if positionalIndex >= len(cmd.Positionals) {
			return p.usageError(cmd, "unrecognized arguments: %s", token)
		}
		pos := cmd.Positionals[positionalIndex]
		if err := p.assignPositional(cmd, pos, token, args); err != nil {
			return err
		}
		if !pos.Variadic {
			positionalIndex++
		}
	}

	missing := []string{}
	for _, option := range cmd.Options {
		if !option.Required || args.provided[option.Dest] {
			continue
		}
		missing = append(missing, option.name())
	}
	for _, pos := range cmd.Positionals {
		if !pos.Required {
			continue
		}
		if _, ok := args.values[pos.Name]; !ok {
			missing = append(missing, pos.Name)
		}
	}
	if len(missing) > 0 {
		return p.usageError(cmd, "the following arguments are required: %s", strings.Join(missing, ", "))
	}
	if len(cmd.Subcommands) > 0 {
		if selected == nil {
			if cmd.DefaultSub == "" {
				return p.usageError(cmd, "the following arguments are required: %s", cmd.SubDest)
			}
			selected = cmd.findSubcommand(cmd.DefaultSub)
			remainder = nil
		}
		return p.parseLevel(selected, remainder, args)
	}
	return nil
}

// assignOption stores one option value; label is the option string the token
// resolved to (an abbreviated prefix reports the full name, like CLI parser).
func (p *Parser) assignOption(cmd *command, option *Option, label, raw string, args *Args) error {
	if err := p.checkChoice(cmd, label, raw, option.Choices); err != nil {
		return err
	}
	switch option.Kind {
	case kindInt:
		number, err := strconv.Atoi(raw)
		if err != nil {
			return p.usageError(cmd, "argument %s: invalid int value: '%s'", label, raw)
		}
		args.values[option.Dest] = number
	default:
		args.values[option.Dest] = raw
	}
	args.provided[option.Dest] = true
	return nil
}

func (p *Parser) assignPositional(cmd *command, pos *positional, raw string, args *Args) error {
	if err := p.checkChoice(cmd, pos.Name, raw, pos.Choices); err != nil {
		return err
	}
	if pos.Variadic {
		values, _ := args.values[pos.Name].([]string)
		args.values[pos.Name] = append(values, raw)
	} else {
		args.values[pos.Name] = raw
	}
	args.provided[pos.Name] = true
	return nil
}

func (p *Parser) checkChoice(cmd *command, label, raw string, choices []string) error {
	if len(choices) == 0 {
		return nil
	}
	for _, choice := range choices {
		if choice == raw {
			return nil
		}
	}
	return &UsageError{
		Usage:   p.usage(cmd),
		Message: fmt.Sprintf("argument %s: invalid choice: '%s' (choose from %s)", label, raw, quoteChoices(choices)),
	}
}

func (p *Parser) usageError(cmd *command, format string, values ...any) error {
	return &UsageError{
		Usage:   p.usage(cmd),
		Message: fmt.Sprintf(format, values...),
	}
}

func quoteChoices(choices []string) string {
	quoted := make([]string, 0, len(choices))
	for _, choice := range choices {
		quoted = append(quoted, "'"+choice+"'")
	}
	return strings.Join(quoted, ", ")
}

// looksLikeOption mirrors CLI parser's rule for "is this the next value or the
// next flag?"; negative numbers stay values because no option looks like one.
func looksLikeOption(token string) bool {
	if len(token) < 2 || !strings.HasPrefix(token, "-") {
		return false
	}
	char := token[1]
	if char >= '0' && char <= '9' || char == '.' {
		return false
	}
	return true
}

// usage renders the full CLI parser usage line for one command.
func (p *Parser) usage(cmd *command) string {
	var builder strings.Builder
	builder.WriteString("usage: ")
	builder.WriteString(p.Prog)
	for _, part := range cmd.Path {
		builder.WriteString(" ")
		builder.WriteString(part)
	}
	builder.WriteString(" [-h]")
	for _, option := range cmd.Options {
		builder.WriteString(" ")
		builder.WriteString(option.usage())
	}
	for _, pos := range cmd.Positionals {
		builder.WriteString(" ")
		builder.WriteString(pos.usage())
	}
	if len(cmd.Subcommands) > 0 {
		builder.WriteString(" {")
		builder.WriteString(strings.Join(cmd.subNames(), ","))
		builder.WriteString("} ...")
	}
	return builder.String()
}

// help renders one command the way CLI parser does: usage, the description (the
// root's description only appears on the root parser; subparsers have none),
// positionals, then options.
func (p *Parser) help(cmd *command) string {
	var builder strings.Builder
	builder.WriteString(p.usage(cmd))
	builder.WriteString("\n")
	description := cmd.Description
	if description == "" && cmd == p.root {
		description = p.Description
	}
	if description != "" {
		builder.WriteString("\n")
		builder.WriteString(description)
		builder.WriteString("\n")
	}
	hasPositionals := len(cmd.Positionals) > 0 || len(cmd.Subcommands) > 0
	if hasPositionals {
		builder.WriteString("\npositional arguments:\n")
		for _, pos := range cmd.Positionals {
			builder.WriteString(fmt.Sprintf("  %-20s %s\n", pos.label(), pos.Help))
		}
		if len(cmd.Subcommands) > 0 {
			builder.WriteString(fmt.Sprintf("  %-20s\n", "{"+strings.Join(cmd.subNames(), ",")+"}"))
			for _, sub := range cmd.Subcommands {
				if sub.Help == "" {
					// CLI parser only lists subcommands that were given a help=
					// (the bare {a,b} line still shows every choice).
					continue
				}
				builder.WriteString(fmt.Sprintf("    %-18s %s\n", sub.Name, sub.Help))
			}
		}
	}
	builder.WriteString("\noptions:\n")
	builder.WriteString(fmt.Sprintf("  %-20s %s\n", "-h, --help", helpOption.Help))
	if cmd == p.root {
		builder.WriteString("  --version             show program's version number and exit\n")
	}
	for _, option := range cmd.Options {
		if option.Dest == "__version__" {
			continue
		}
		builder.WriteString(fmt.Sprintf("  %-20s %s\n", option.label(), option.Help))
	}
	return builder.String()
}

// ------------------------------------------------------------ parser table

func stringOption(names []string, dest string, def any, help string) *Option {
	return &Option{Names: names, Dest: dest, Kind: kindString, Default: def, Help: help}
}

func intOption(names []string, dest string, def int, help string) *Option {
	return &Option{Names: names, Dest: dest, Kind: kindInt, Default: def, Help: help}
}

func floatOption(names []string, dest string, def float64, help string) *Option {
	return &Option{Names: names, Dest: dest, Kind: kindString,
		Default: strconv.FormatFloat(def, 'g', -1, 64), Help: help}
}

func flagOption(names []string, dest string, def any, help string) *Option {
	return &Option{Names: names, Dest: dest, Kind: kindFlagTrue, Default: def, Help: help}
}

func choiceOption(names []string, dest string, choices []string, def any, help string) *Option {
	return &Option{Names: names, Dest: dest, Kind: kindString, Choices: choices, Default: def, Help: help}
}

func requiredOption(names []string, dest, metavar, help string) *Option {
	return &Option{Names: names, Dest: dest, Kind: kindString, Required: true, Metavar: metavar, Help: help}
}

func commonOptions() []*Option {
	return []*Option{
		stringOption([]string{"--config"}, "config", nil,
			"config file (default: $XDG_CONFIG_HOME/rolling-memory/config.toml)"),
		stringOption([]string{"--db"}, "db", nil, "database path"),
		stringOption([]string{"--scope"}, "scope", nil, "memory scope"),
		choiceOption([]string{"--mode"}, "mode", []string{"local"}, nil,
			"override mode for this command (local only)"),
		flagOption([]string{"--json"}, "json", false, ""),
	}
}

// BuildParser returns the full CLI parser-compatible command table.
func BuildParser() *Parser {
	parser := &Parser{
		Prog:        "rolling-memory",
		Description: "Local-first persistent memory for coding agents (SQLite, single `recall` MCP tool).",
	}
	root := &command{
		SubDest: "command",
		Options: []*Option{{
			Names: []string{"--version"}, Dest: "__version__", Kind: kindFlagTrue,
			Help: "show program's version number and exit",
		}},
	}

	initCmd := &command{
		Name: "init", Path: []string{"init"}, Help: "create the database and show paths",
		Options: append(commonOptions(),
			flagOption([]string{"--write-config"}, "write_config", false,
				"write a commented default config file"),
			flagOption([]string{"--project"}, "project", false,
				"use ./.rolling-memory/memory.db for this project"),
			stringOption([]string{"--portable"}, "portable", "",
				"assemble a self-contained USB portable payload at DIR"),
		),
	}

	modeCmd := &command{
		Name: "mode", Path: []string{"mode"}, Help: "show the local storage mode",
		Options: commonOptions(),
		Positionals: []*positional{{
			Name: "name", Choices: []string{"local"},
		}},
	}

	configCmd := &command{
		Name: "config", Path: []string{"config"}, Help: "show or edit configuration",
		SubDest: "config_command",
	}
	configCmd.Subcommands = []*command{
		{Name: "show", Path: []string{"config", "show"}, Options: commonOptions()},
		{Name: "path", Path: []string{"config", "path"}, Options: commonOptions()},
		{
			Name: "set", Path: []string{"config", "set"}, Options: commonOptions(),
			Positionals: []*positional{
				{Name: "key", Help: "dotted key, e.g. mode.name", Required: true},
				{Name: "value", Required: true},
			},
		},
	}

	recallCmd := &command{
		Name: "recall", Path: []string{"recall"}, Help: "read memory as bands x chains",
		Options: append(commonOptions(),
			stringOption([]string{"--q", "--cue"}, "cue", "", ""),
			choiceOption([]string{"--reader"}, "reader", []string{"owner", "deliverable"}, nil, ""),
			choiceOption([]string{"--result-mode"}, "result_mode", []string{"full", "probe", "recent"}, "full", ""),
			&Option{Names: []string{"--bands"}, Dest: "bands", Kind: kindInt, Default: nil,
				Help: "maximum delivered bands (also raises the candidate depth when larger than --raw-limit)"},
			&Option{Names: []string{"--nodes"}, Dest: "nodes", Kind: kindInt, Default: nil,
				Help: "maximum delivered nodes across all bands"},
			&Option{Names: []string{"--chars"}, Dest: "chars", Kind: kindInt, Default: nil,
				Help: "delivery character budget"},
			intOption([]string{"--offset"}, "offset", 0, "band pagination offset"),
			flagOption([]string{"--include-raw"}, "include_raw", nil, "include raw source hits in the result"),
			&Option{Names: []string{"--no-raw"}, Dest: "include_raw", Kind: kindFlagFalse, Help: "omit raw source hits"},
			intOption([]string{"--raw-limit"}, "raw_limit", 10, "raw-hit limit (default 10, maximum 64)"),
			stringOption([]string{"--time-ref"}, "time_ref", nil,
				"reference time for relative expressions (ISO date or epoch)"),
			&Option{Names: []string{"--budget-ms"}, Dest: "budget_ms", Kind: kindInt, Default: nil, Help: ""},
			flagOption([]string{"--no-crystallize"}, "no_crystallize", false,
				"read-only recall: do not record this query as an L3 noetic entry (debug consoles, probes)"),
			flagOption([]string{"--stdin-batch"}, "stdin_batch", false,
				"serve multiple recall requests over stdin JSONL ({\"req_id\",\"q\",\"raw_limit\"}) on one warm process; one JSON response per line"),
		),
	}

	recentCmd := &command{
		Name: "recent", Path: []string{"recent"}, Help: "show the most recent memory units",
		Options: append(commonOptions(),
			intOption([]string{"--limit"}, "limit", 10, ""),
			choiceOption([]string{"--reader"}, "reader", []string{"owner", "deliverable"}, nil, ""),
		),
	}

	// evalOptions 同时挂在 eval 父命令（裸调用 = 全流程）与全部阶段子命令上：
	// 每个阶段的检索/模型/数据面参数与一把梭完全一致，子命令只决定推进哪一段状态机。
	evalOptions := []*Option{
		stringOption([]string{"--config"}, "config", nil,
			"config file (default: $XDG_CONFIG_HOME/rolling-memory/config.toml)"),
		flagOption([]string{"--json"}, "json", false, ""),
		stringOption([]string{"--job"}, "job", nil,
			"evaluation job id (default for ingest/run/grade/status: latest job of the suite/store)"),
		choiceOption([]string{"--suite"}, "suite", []string{"locomo", "atm", "atm-probes"}, "locomo",
			"benchmark suite (locomo | atm | atm-probes)"),
		choiceOption([]string{"--tier"}, "tier", []string{"1540", "adversarial-446", "hard", "1013", "absence", "conflict"}, "1540",
			"question tier (LoCoMo: 1540 = official cat1-4 set, adversarial-446 = official cat5 adversarial set; ATM-Bench: hard/1013; atm-probes: absence/conflict; the retired LoCoMo convenience tiers 100/200 are no longer offered)"),
		stringOption([]string{"--eval-data"}, "eval_data", nil,
			"evaluation data directory (default: evaldata next to the binary)"),
		stringOption([]string{"--probes"}, "probes", nil,
			"adversarial probe directory (MANIFEST.json + absence/conflict JSONL; used with --suite atm-probes)"),
		stringOption([]string{"--fetch"}, "fetch", nil,
			"download upstream benchmark data, rebuild and verify it, then stop (add --suite to run the evaluation in the same invocation; atm = ATM-Bench from Hugging Face, ~35 MB, rebuilt locally into the pinned form; locomo = LoCoMo10 annotations from the upstream repository, ~2.8 MB, rebuilt into the official cat1-4 question set)"),
		flagOption([]string{"--fetch-only"}, "fetch_only", false,
			"only download + rebuild + verify the upstream data, then exit"),
		stringOption([]string{"--fetch-revision"}, "fetch_revision", nil,
			"override the pinned upstream dataset revision (advanced; default: the revision pinned in this binary)"),
		flagOption([]string{"--accept-upstream-license"}, "accept_upstream_license", false,
			"accept the upstream data license (CC BY-NC 4.0) before downloading"),
		stringOption([]string{"--out-dir"}, "out_dir", nil,
			"report directory (default: <state>/eval/out; jobs live in <out-dir>/jobs)"),
		stringOption([]string{"--eval-store"}, "eval_store", nil,
			"isolated plaintext JSON vault (default: <state>/eval/eval-locomo.json or eval-atm.json)"),
		flagOption([]string{"--oracle"}, "oracle", false,
			"gold evidence as context (answerer ceiling)"),
		choiceOption([]string{"--answerer"}, "answerer", []string{"none", "llm"}, nil,
			"answerer side of the frozen protocol (default: [eval] answerer)"),
		choiceOption([]string{"--judge"}, "judge", []string{"off", "llm"}, nil,
			"judge side of the frozen protocol (default: [eval] judge)"),
		stringOption([]string{"--eval-model"}, "eval_model", nil,
			"override [eval] model (wire name; e.g. the gateway-side name)"),
		stringOption([]string{"--eval-base-url"}, "eval_base_url", nil,
			"override [eval] base_url (any OpenAI-compatible /chat/completions root)"),
		{Names: []string{"--limit"}, Dest: "limit", Kind: kindInt, Default: nil,
			Help: "cap questions"},
		intOption([]string{"--offset"}, "offset", 0, "question offset"),
		intOption([]string{"--workers"}, "workers", 0, "parallel questions (default: [eval] workers)"),
		intOption([]string{"--timeout"}, "timeout", 0, "per-call timeout seconds"),
		intOption([]string{"--max-tokens"}, "max_tokens", 0, "per-call max tokens"),
		intOption([]string{"--context-chars"}, "context_chars", 0, "context character cap"),
		intOption([]string{"--top-k", "--raw-limit"}, "raw_limit", 0, "engine hit cap"),
		intOption([]string{"--corpus-limit"}, "corpus_limit", 0, "ingest only N corpus rows (smoke)"),
		intOption([]string{"--progress-every"}, "progress_every", 20, "progress line interval"),
		flagOption([]string{"--rebuild"}, "rebuild", false, "reset the isolated evaluation scope (purge every remembered unit), then re-ingest the corpus"),
		flagOption([]string{"--reuse-prepare"}, "reuse_prepare", false, "reuse an identical isolated corpus only after exact manifest-set verification"),
		flagOption([]string{"--verify-prepare"}, "verify_prepare", false, "verify exact manifest/corpus-unit equality without ingesting or scoring"),
		choiceOption([]string{"--pulse-tokenizer"}, "pulse_tokenizer", []string{"product", "standard", "trigram"}, "product",
			"evaluation tokenizer arm: product = default max-capability product tokenizer (128k BPE substring pieces + companion-word expansion + unit/date beacons; default) | standard = SDK native surface+subword (diagnostic) | trigram = trigram-window segmenter (diagnostic)"),
		flagOption([]string{"--no-verify"}, "no_verify", false,
			"skip the sha256 pin check of the evaluation corpus and question set (diagnostic only; readings produced this way are not comparable to published baselines)"),
		floatOption([]string{"--denoise-ratio"}, "denoise_ratio", 0.0,
			"分形干句降噪比例 (0.0=零损, 0.25=安全, 0.50=推荐, 0.75=激进)"),
	}
	evalStage := func(name, help string) *command {
		return &command{Name: name, Path: []string{"eval", name}, Help: help, Options: evalOptions}
	}
	evalCmd := &command{
		Name: "eval", Path: []string{"eval"},
		Help:    "isolated local evaluation (jobs: import → ingest → run → grade)",
		Options: evalOptions, SubDest: "eval_command", DefaultSub: "all",
		Subcommands: []*command{
			evalStage("all", "full pipeline: import → ingest → run → grade (same as a bare `rmem eval`)"),
			evalStage("import", "reserved: corpus verify/materialize (standalone stage not implemented; `rmem eval` runs the full pipeline)"),
			evalStage("ingest", "reserved: corpus ingest into the isolated scope (standalone stage not implemented; `rmem eval` runs the full pipeline)"),
			evalStage("run", "reserved: recall-only run, no LLM (standalone stage not implemented; `rmem eval` runs the full pipeline)"),
			evalStage("grade", "reserved: answer + judge with your own OpenAI-compatible LLM (standalone stage not implemented; `rmem eval` runs the full pipeline)"),
			evalStage("status", "show one recorded evaluation run (--job or the latest) from the out-dir ledger (read-only)"),
			evalStage("jobs", "list recorded evaluation runs from the out-dir ledger (read-only, never triggers a run)"),
		},
	}

	importCmd := &command{
		Name: "import", Path: []string{"import"}, Help: "import a transcript (JSONL or text)",
		Options: append(commonOptions(),
			stringOption([]string{"--session"}, "session", nil, ""),
			choiceOption([]string{"--audience"}, "audience",
				[]string{"shared", "public"}, "shared", "本地金库为单人本地，仅支持 shared/public；受限受众在写入前拒绝"),
		),
		Positionals: []*positional{{Name: "path", Required: true}},
	}

	syncCmd := &command{
		Name: "sync", Path: []string{"sync"}, Help: "local alignment status and reconcile (zero network egress)",
		SubDest: "sync_command",
	}
	syncStatusCmd := &command{
		Name: "status", Path: []string{"sync", "status"}, Options: append(commonOptions(),
			stringOption([]string{"--harness"}, "harness", nil, "optional harness id filter (zcode/claude-code/codex/all)")),
	}
	syncReconcileCmd := &command{
		Name: "reconcile", Path: []string{"sync", "reconcile"}, Options: append(commonOptions(),
			stringOption([]string{"--layer"}, "layer", "all", "layer to reconcile (all|episodic|background|noetic)"),
			stringOption([]string{"--harness"}, "harness", nil, "optional harness id filter (zcode/claude-code/codex/all)"),
			intOption([]string{"--limit"}, "limit", 50, "max units to reconcile per layer in one pass"),
			intOption([]string{"--scan-limit"}, "scan_limit", 2000, "max harness turns scanned per harness in one foreground pass (1-50000)"),
			flagOption([]string{"--rebuild-episodic"}, "rebuild_episodic", false, "rebuild L2 episodic as one unit per speech segment (dry-run unless --yes)"),
			flagOption([]string{"--yes"}, "yes", false, "confirm the episodic rebuild (purges superseded units)")),
	}
	syncWatchCmd := &command{
		Name: "watch", Path: []string{"sync", "watch"}, Options: append(commonOptions(),
			intOption([]string{"--interval-sec"}, "interval_sec", 10, "poll cadence in seconds"),
			flagOption([]string{"--once"}, "once", false, "run one single-flight auto-watch pass and exit"),
			flagOption([]string{"--pause"}, "pause", false, "pause background episodic scanning and index backfilling"),
			flagOption([]string{"--resume"}, "resume", false, "resume background episodic scanning and index backfilling"),
			flagOption([]string{"--step"}, "step", false, "advance one bounded pass of episodic index backfilling and source scanning")),
	}
	syncPruneCmd := &command{
		Name: "prune", Path: []string{"sync", "prune"}, Options: append(commonOptions(),
			intOption([]string{"--limit"}, "limit", 20000, "max pointer rows to classify in one pass"),
			flagOption([]string{"--include-unreadable"}, "include_unreadable", false, "also retire pointers whose source session or transcript is gone"),
			flagOption([]string{"--reanchor-cross-source"}, "reanchor_cross_source", false, "also re-anchor drifted rows whose verified content lives in another session (explicit opt-in)"),
			flagOption([]string{"--apply"}, "apply", false, "retire the units whose content the turn no longer yields (dry-run without it)"),
			flagOption([]string{"--yes"}, "yes", false, "confirm the prune (required with --apply)")),
	}
	syncCmd.Subcommands = []*command{syncStatusCmd, syncReconcileCmd, syncWatchCmd, syncPruneCmd}

	backgroundCmd := &command{
		Name: "background", Path: []string{"background"}, Help: "mount, read and synchronize local document directories",
		SubDest: "background_command",
	}
	backgroundCmd.Subcommands = []*command{
		{
			Name: "add", Path: []string{"background", "add"}, Options: append(commonOptions(),
				requiredOption([]string{"--path"}, "path", "DIR", "local directory to mount and ingest"),
				stringOption([]string{"--ext"}, "ext", "", "comma-separated file extensions to include (e.g. .md,.txt,.pdf; * for all)")),
		},
		{Name: "sync", Path: []string{"background", "sync"}, Options: commonOptions()},
		{
			Name: "remove", Path: []string{"background", "remove"}, Options: append(commonOptions(),
				requiredOption([]string{"--path"}, "path", "DIR", "mounted directory to purge and unmount"),
				flagOption([]string{"--dry-run"}, "dry_run", false, "preview the unmount (re-binds vs purges vs citation impact) without writing")),
		},
		{Name: "list", Path: []string{"background", "list"}, Options: commonOptions()},
	}

	mcpCmd := &command{
		Name: "mcp", Path: []string{"mcp"},
		Help: "run the MCP server (recall); --http serves the URL transports",
		Options: append(commonOptions(),
			flagOption([]string{"--http"}, "http", false,
				"serve the MCP URL transports (streamable HTTP + legacy SSE) instead of stdio"),
			flagOption([]string{"--daemon"}, "daemon", false,
				"serve the shared local Unix domain socket engine daemon"),
			stringOption([]string{"--addr"}, "addr", "",
				"listen address for --http (default 127.0.0.1:8765)"),
			stringOption([]string{"--path"}, "path", "",
				"streamable HTTP endpoint path (default /mcp)")),
	}

	buildCmd := &command{
		Name: "build", Path: []string{"build"},
		Help:    "build the local retrieval snapshot (.zcsr) for the current vault",
		Options: commonOptions(),
	}

	ingestdCmd := &command{
		Name: "ingestd", Path: []string{"ingestd"},
		Help:    "internal: background ingest worker (scan, heal, snapshot supervisor)",
		Options: commonOptions(),
	}

	scanCmd := &command{
		Name: "scan", Path: []string{"scan"},
		Help:    "control the background scan worker (pause, resume, status)",
		Options: commonOptions(), SubDest: "scan_action", DefaultSub: "status",
	}
	scanCmd.Subcommands = []*command{
		{Name: "pause", Path: []string{"scan", "pause"}, Options: commonOptions()},
		{Name: "resume", Path: []string{"scan", "resume"}, Options: commonOptions()},
		{Name: "status", Path: []string{"scan", "status"}, Options: commonOptions()},
	}

	recoverCmd := &command{
		Name: "recover", Path: []string{"recover"},
		Help: "internal: additive episodic source scan (no reconcile or prune)",
		Options: append(commonOptions(),
			intOption([]string{"--passes"}, "passes", 10, "scan passes"),
			intOption([]string{"--limit"}, "limit", 500, "max turns per pass"),
			intOption([]string{"--budget-sec"}, "budget_sec", 60, "per-pass wall budget in seconds")),
	}

	hookCmd := &command{
		Name: "hook", Path: []string{"hook"}, Help: "run a harness hook (fail-open)",
		Options: []*Option{
			flagOption([]string{"--json"}, "json", false, ""),
			{Names: []string{"--budget-ms"}, Dest: "budget_ms", Kind: kindInt, Default: nil, Help: ""},
			stringOption([]string{"--config"}, "config", nil, ""),
		},
		Positionals: []*positional{{
			Name: "event", Choices: []string{"recall"}, Required: true,
		}},
	}

	statusCmd := &command{
		Name: "status", Path: []string{"status"}, Help: "show counts, mode and storage paths",
		Options: commonOptions(),
	}
	doctorCmd := &command{
		Name: "doctor", Path: []string{"doctor"}, Help: "environment and integrity diagnostics",
		Options: commonOptions(),
	}
	checkCmd := &command{
		Name: "check", Path: []string{"check"}, Help: "run integrity checks; exit 1 on errors",
		Options: commonOptions(),
	}
	maintainCmd := &command{
		Name: "maintain", Path: []string{"maintain"},
		Help:    "user maintenance: inspect reclaimable space, clean obsolete backups/caches, compact the vault",
		Options: commonOptions(), SubDest: "maintain_command", DefaultSub: "status",
	}
	maintainCmd.Subcommands = []*command{
		{Name: "status", Path: []string{"maintain", "status"}, Options: commonOptions()},
		{
			Name: "clean", Path: []string{"maintain", "clean"}, Options: append(commonOptions(),
				intOption([]string{"--keep"}, "keep", maintainDefaultBackupKeep, "keep the newest N backups per (dir, family)"),
				flagOption([]string{"--caches"}, "caches", false, "also clear regenerable state caches (daemon memory snapshots are dropped first)"),
				flagOption([]string{"--purge-engine-cache"}, "purge_engine_cache", false, "also delete the engine disk-cluster cache (.zcsr); the next recall rebuilds it (minutes, multi-GB peak)"),
				flagOption([]string{"--apply"}, "apply", false, "execute the cleanup (dry-run without it)"),
				flagOption([]string{"--yes"}, "yes", false, "confirm the cleanup (required with --apply)")),
		},
		{
			Name: "compact", Path: []string{"maintain", "compact"}, Options: append(commonOptions(),
				flagOption([]string{"--apply"}, "apply", false, "execute the compaction (dry-run without it)"),
				flagOption([]string{"--yes"}, "yes", false, "confirm the compaction (required with --apply)"),
				flagOption([]string{"--no-daemon-restart"}, "no_daemon_restart", false, "leave the shared engine daemon stopped after compaction")),
		},
	}

	perfCmd := &command{
		Name: "perf", Path: []string{"perf"},
		Help:    "measurement harness: per-unit storage cost at several corpus sizes",
		Options: commonOptions(), SubDest: "perf_command", DefaultSub: "slope",
	}
	perfCmd.Subcommands = []*command{
		{
			Name: "slope", Path: []string{"perf", "slope"}, Options: append(commonOptions(),
				stringOption([]string{"--files"}, "files", "100,300,600", "comma-separated corpus sizes measured on throwaway vaults"),
				stringOption([]string{"--ablate"}, "ablate", "", "ablation applied inside the throwaway vault only (revision-triggers)"),
				stringOption([]string{"--dir"}, "dir", nil, "keep measurement artifacts in this directory")),
		},
		{
			Name: "recall", Path: []string{"perf", "recall"}, Options: append(commonOptions(),
				stringOption([]string{"--files"}, "files", "100,300,600", "comma-separated corpus sizes measured on throwaway vaults"),
				intOption([]string{"--queries"}, "queries", 20, "queries per scale (1-200)"),
				stringOption([]string{"--profile"}, "profile", "", "storage profile for the throwaway vault (local, cache64, mmap256, cache64+mmap256)"),
				stringOption([]string{"--dir"}, "dir", nil, "keep measurement artifacts in this directory")),
		},
	}

	daemonCmd := &command{
		Name: "daemon", Path: []string{"daemon"},
		Help:    "control the shared local engine daemon (status, start, stop, restart)",
		Options: commonOptions(), SubDest: "daemon_action", DefaultSub: "status",
	}
	daemonCmd.Subcommands = []*command{
		{Name: "status", Path: []string{"daemon", "status"}, Options: commonOptions()},
		{Name: "start", Path: []string{"daemon", "start"}, Options: commonOptions()},
		{Name: "stop", Path: []string{"daemon", "stop"}, Options: commonOptions()},
		{Name: "restart", Path: []string{"daemon", "restart"}, Options: commonOptions()},
	}

	installCmd := &command{
		Name: "install", Path: []string{"install"}, Help: "wire this memory core into agent harnesses (skill + MCP + optional hooks/instructions)",
		Options: []*Option{
			flagOption([]string{"--list"}, "list", false, "show the harness adapter table"),
			flagOption([]string{"--dry-run"}, "dry_run", false,
				"show planned writes without touching files"),
			flagOption([]string{"--uninstall"}, "uninstall", false,
				"remove what install wrote"),
			flagOption([]string{"--check"}, "check", false,
				"verify registration and the MCP handshake"),
			flagOption([]string{"--project"}, "project", false,
				"write into the current project instead of user scope"),
			stringOption([]string{"--name"}, "name", "rolling-memory",
				"MCP server name (default: rolling-memory)"),
			stringOption([]string{"--skill-name"}, "skill_name", nil,
				"installed skill name (default: same as --name)"),
			stringOption([]string{"--binary"}, "binary", nil,
				"launcher command to register (default: generated shim)"),
			stringOption([]string{"--skill-file"}, "skill_file", nil,
				"SKILL.md source (default: skills/rolling-memory/SKILL.md)"),
			stringOption([]string{"--lang"}, "lang", nil,
				"language for installed skill, instructions, and admin console default (en | zh-CN; default: the saved preference, else en)"),
			stringOption([]string{"--db"}, "db", nil,
				"ROLLING_MEMORY_DB written into the MCP entry"),
			stringOption([]string{"--scope"}, "scope", nil,
				"ROLLING_MEMORY_SCOPE written into the MCP entry"),
			flagOption([]string{"--mcp-only"}, "mcp_only", false,
				"wire only the MCP server (skip skill, hooks, and instruction files)"),
			flagOption([]string{"--with-hooks"}, "with_hooks", false,
				"wire the per-prompt recall nudge (UserPromptSubmit)"),
			flagOption([]string{"--with-instructions"}, "with_instructions", false,
				"append the marker block to instruction files"),
			flagOption([]string{"--desktop-shortcut"}, "desktop_shortcut", false,
				"also create a desktop launcher that opens the local web console"),
			flagOption([]string{"--force"}, "force", false,
				"overwrite same-named foreign entries"),
			flagOption([]string{"--dedupe"}, "dedupe", false,
				"remove this tool's duplicate skill, MCP entry, and hook when a native plugin provides the same surface"),
			stringOption([]string{"--profile"}, "profile", "web",
				"dsh profile for the MCP patch (default: web)"),
			flagOption([]string{"--json"}, "json", false, ""),
		},
		Positionals: []*positional{{
			Name: "harnesses", Variadic: true,
			Help: "harness ids (default: all detected); see --list",
		}},
	}

	ejectCmd := &command{
		Name: "eject", Path: []string{"eject"},
		Help:    "flush and truncate the SQLite WAL and verify integrity before removable media eject",
		Options: commonOptions(),
	}

	efficacyCmd := &command{
		Name: "efficacy", Path: []string{"efficacy"},
		Help: "evaluate cross-harness recall efficacy, round-trip savings and net token ROI",
		Options: append(commonOptions(),
			stringOption([]string{"--harness"}, "harness", "",
				"filter by harness id (zcode/claude-code/codex/cursor/windsurf/cline/dsh/codebuddy/workbuddy/doubao)"),
			intOption([]string{"--limit"}, "limit", 20,
				"maximum recent session evaluations to list"),
			intOption([]string{"--budget-seconds"}, "budget_seconds", 0,
				"soft wall-clock budget for settling unsettled turns; the run finalizes and persists a partial snapshot once exhausted (0 = unbounded)"),
			flagOption([]string{"--card"}, "card", false,
				"render a compact shareable ASCII ROI receipt card"),
		),
	}

	panelCmd := &command{
		Name: "panel", Path: []string{"panel"},
		Help: "open the local web console (backend :9006 + web frontend :8008; fetches the console on first use)",
		Options: append(commonOptions(),
			flagOption([]string{"--no-open"}, "no_open", false,
				"start the console without opening the browser"),
			flagOption([]string{"--update"}, "update", false,
				"check the release channel and update console backend/frontend assets if newer digests exist"),
			stringOption([]string{"--release-base"}, "release_base", nil,
				"override the release channel URL (default: official GitHub releases; ROLLING_MEMORY_RELEASE_BASE also supported)"),
			intOption([]string{"--api-port"}, "api_port", 0,
				"console backend port (default: 9006; RM_PANEL_API_PORT overrides)"),
			intOption([]string{"--web-port"}, "web_port", 0,
				"console web port (default: 8008; RM_PANEL_WEB_PORT overrides)"),
			stringOption([]string{"--serve-web"}, "serve_web", nil,
				"internal: serve console web assets from this directory in the foreground"),
		),
		Positionals: []*positional{{
			Name: "verbs", Variadic: true, Choices: []string{"start", "stop", "restart", "status"},
			Help: "panel action (default: start)",
		}},
	}

	updateOptions := append(commonOptions(),
		flagOption([]string{"--check"}, "check", false,
			"only check whether a newer release is available without modifying files"),
		flagOption([]string{"--tags-only"}, "tags_only", false,
			"with --check, resolve the newest tag without downloading or comparing release artifacts"),
		flagOption([]string{"--quiet"}, "quiet", false,
			"suppress success output (for scheduled runs)"),
		flagOption([]string{"--notify"}, "notify", false,
			"send a desktop notification when a newer tag is available"),
		flagOption([]string{"--auto"}, "auto", false,
			"apply the saved update.auto policy (off, notify or apply) for a background run"),
		stringOption([]string{"--schedule"}, "schedule", nil,
			"manage the optional OS-level periodic update job: install | remove | status"),
		choiceOption([]string{"--channel"}, "channel", []string{"stable", "beta", "dev"}, nil,
			"release channel: stable (published installs), beta (prerelease tags), dev (main head; source rebuild)"),
		choiceOption([]string{"--source"}, "source", []string{"github", "gitee"}, nil,
			"release source for this run: github (official releases) or gitee (mainland mirror); update.source sets the default"),
		flagOption([]string{"--force"}, "force", false,
			"re-download and reinstall even when local SHA-256 digests already match"),
		flagOption([]string{"--with-console"}, "with_console", false,
			"also fetch and install the local web console if not yet installed"),
		flagOption([]string{"--no-console"}, "no_console", false,
			"skip checking or updating the local web console assets"),
		flagOption([]string{"--no-install"}, "no_install", false,
			"skip refreshing host harness integrations after updating the binary"),
		flagOption([]string{"--defer-console-restart"}, "defer_console_restart", false,
			"update console assets in place without synchronously stopping the running console process"),
		stringOption([]string{"--release-base"}, "release_base", nil,
			"override the release channel URL (default: official GitHub releases; ROLLING_MEMORY_RELEASE_BASE also supported)"),
	)
	updateCmd := &command{
		Name: "update", Path: []string{"update"},
		Help:    "self-update the CLI binary, web console assets and host integrations (SHA-256 verified, no git required)",
		Options: updateOptions,
	}
	// engine 是控制台与自动化专用的内部已核验守护通道（请求 JSON 走 stdin、响应 JSON 走
	// stdout）：不在用户文档中宣传，也不改变任何用户面命令的行为。
	engineCmd := &command{
		Name: "engine", Path: []string{"engine"},
		Help:    "internal verified local-daemon channel for the web console and automation (request JSON on stdin, response JSON on stdout)",
		Options: commonOptions(),
		Positionals: []*positional{{
			Name: "op", Choices: []string{"recall", "hydrate", "documents", "graph", "invalidate"}, Required: true,
		}},
	}

	root.Subcommands = []*command{
		initCmd, modeCmd, configCmd, recallCmd, recentCmd,
		importCmd, syncCmd, backgroundCmd, mcpCmd, buildCmd, ingestdCmd, scanCmd, recoverCmd, hookCmd,
		statusCmd, doctorCmd, checkCmd, installCmd, evalCmd,
		ejectCmd, efficacyCmd, panelCmd, updateCmd,
		maintainCmd, perfCmd, daemonCmd, engineCmd,
	}
	for _, sub := range root.Subcommands {
		sub.Path = []string{sub.Name}
	}
	parser.root = root
	return parser
}
