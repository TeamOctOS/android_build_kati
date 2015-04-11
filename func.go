package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// TODO(ukai): move in var.go?
type oldVar struct {
	name  string
	value Var
}

func newOldVar(ev *Evaluator, name string) oldVar {
	return oldVar{
		name:  name,
		value: ev.outVars.Lookup(name),
	}
}

func (old oldVar) restore(ev *Evaluator) {
	if old.value.IsDefined() {
		ev.outVars.Assign(old.name, old.value)
		return
	}
	delete(ev.outVars, old.name)
}

// Func is a make function.
// http://www.gnu.org/software/make/manual/make.html#Functions

// Func is make builtin function.
type Func interface {
	// Arity is max function's arity.
	// ',' will not be handled as argument separator more than arity.
	// 0 means varargs.
	Arity() int

	// AddArg adds value as an argument.
	AddArg(Value)

	// SetString sets original string of the func.
	SetString(string)

	Value
}

var (
	funcMap = map[string]func() Func{
		"patsubst":   func() Func { return &funcPatsubst{} },
		"strip":      func() Func { return &funcStrip{} },
		"subst":      func() Func { return &funcSubst{} },
		"findstring": func() Func { return &funcFindstring{} },
		"filter":     func() Func { return &funcFilter{} },
		"filter-out": func() Func { return &funcFilterOut{} },
		"sort":       func() Func { return &funcSort{} },
		"word":       func() Func { return &funcWord{} },
		"wordlist":   func() Func { return &funcWordlist{} },
		"words":      func() Func { return &funcWords{} },
		"firstword":  func() Func { return &funcFirstword{} },
		"lastword":   func() Func { return &funcLastword{} },

		"join":      func() Func { return &funcJoin{} },
		"wildcard":  func() Func { return &funcWildcard{} },
		"dir":       func() Func { return &funcDir{} },
		"notdir":    func() Func { return &funcNotdir{} },
		"suffix":    func() Func { return &funcSuffix{} },
		"basename":  func() Func { return &funcBasename{} },
		"addsuffix": func() Func { return &funcAddsuffix{} },
		"addprefix": func() Func { return &funcAddprefix{} },
		"realpath":  func() Func { return &funcRealpath{} },
		"abspath":   func() Func { return &funcAbspath{} },

		"shell":   func() Func { return &funcShell{} },
		"call":    func() Func { return &funcCall{} },
		"foreach": func() Func { return &funcForeach{} },
	}
)

func init() {
	/*
			fwrap("findstring", 2, funcFindstring)
			fwrap("filter", 2, funcFilter)
			fwrap("filter-out", 2, funcFilterOut)
			fwrap("sort", 1, funcSort)
			fwrap("word", 2, funcWord)
			fwrap("wordlist", 3, funcWordlist)
			fwrap("words", 1, funcWords)
			fwrap("firstword", 1, funcFirstword)
			fwrap("lastword", 1, funcLastword)
		fwrap("join", 2, funcJoin)
		fwrap("wildcard", 1, funcWildcard)
		fwrap("dir", 1, funcDir)
		fwrap("notdir", 1, funcNotdir)
		fwrap("suffix", 1, funcSuffix)
		fwrap("basename", 1, funcBasename)
		fwrap("addsuffix", 2, funcAddsuffix)
		fwrap("addprefix", 2, funcAddprefix)
		fwrap("realpath", 1, funcRealpath)
		fwrap("abspath", 1, funcAbspath)
	*/
	fwrap("if", 3, funcIf)
	fwrap("and", 0, funcAnd)
	fwrap("or", 0, funcOr)
	fwrap("value", 1, funcValue)
	fwrap("eval", 1, funcEval)
	fwrap("origin", 1, funcOrigin)
	fwrap("flavor", 1, funcFlavor)
	fwrap("info", 1, funcInfo)
	fwrap("warning", 1, funcWarning)
	fwrap("error", 1, funcError)
}

func assertArity(name string, req, n int) {
	if n < req {
		panic(fmt.Sprintf("*** insufficient number of arguments (%d) to function `%s'.", n, name))
	}
}

// A space separated values writer.
type ssvWriter struct {
	w          io.Writer
	needsSpace bool
}

func (sw *ssvWriter) Write(b []byte) {
	if sw.needsSpace {
		sw.w.Write([]byte{' '})
	}
	sw.needsSpace = true
	sw.w.Write(b)
}

func (sw *ssvWriter) WriteString(s string) {
	// TODO: Ineffcient. Nice if we can remove the cast.
	sw.Write([]byte(s))
}

func numericValueForFunc(ev *Evaluator, v Value, funcName string, nth string) int {
	a := bytes.TrimSpace(ev.Value(v))
	n, err := strconv.Atoi(string(a))
	if err != nil || n < 0 {
		Error(ev.filename, ev.lineno, `*** non-numeric %s argument to "%s" function: "%s".`, nth, funcName, a)
	}
	return n
}

type fclosure struct {
	args []Value
	expr string
}

func (c *fclosure) AddArg(v Value) {
	c.args = append(c.args, v)
}

func (c *fclosure) SetString(s string) { c.expr = s }
func (c *fclosure) String() string     { return c.expr }

// http://www.gnu.org/software/make/manual/make.html#Text-Functions
type funcSubst struct{ fclosure }

func (f *funcSubst) Arity() int { return 3 }
func (f *funcSubst) Eval(w io.Writer, ev *Evaluator) {
	assertArity("subst", 3, len(f.args))
	from := ev.Value(f.args[0])
	to := ev.Value(f.args[1])
	text := ev.Value(f.args[2])
	Log("subst from:%q to:%q text:%q", from, to, text)
	w.Write(bytes.Replace(text, from, to, -1))
}

type funcPatsubst struct{ fclosure }

func (f *funcPatsubst) Arity() int { return 3 }
func (f *funcPatsubst) Eval(w io.Writer, ev *Evaluator) {
	assertArity("patsubst", 3, len(f.args))
	pat := ev.Value(f.args[0])
	repl := ev.Value(f.args[1])
	texts := splitSpacesBytes(ev.Value(f.args[2]))
	sw := ssvWriter{w: w}
	for _, text := range texts {
		t := substPatternBytes(pat, repl, text)
		sw.Write(t)
	}
}

type funcStrip struct{ fclosure }

func (f *funcStrip) Arity() int { return 1 }
func (f *funcStrip) Eval(w io.Writer, ev *Evaluator) {
	assertArity("strip", 1, len(f.args))
	text := ev.Value(f.args[0])
	w.Write(bytes.TrimSpace(text))
}

type funcFindstring struct{ fclosure }

func (f *funcFindstring) Arity() int { return 2 }
func (f *funcFindstring) Eval(w io.Writer, ev *Evaluator) {
	assertArity("findstring", 2, len(f.args))
	find := ev.Value(f.args[0])
	text := ev.Value(f.args[1])
	if bytes.Index(text, find) >= 0 {
		w.Write(find)
	}
}

type funcFilter struct{ fclosure }

func (f *funcFilter) Arity() int { return 2 }
func (f *funcFilter) Eval(w io.Writer, ev *Evaluator) {
	assertArity("filter", 2, len(f.args))
	patterns := splitSpacesBytes(ev.Value(f.args[0]))
	texts := splitSpacesBytes(ev.Value(f.args[1]))
	sw := ssvWriter{w: w}
	for _, text := range texts {
		for _, pat := range patterns {
			if matchPatternBytes(pat, text) {
				sw.Write(text)
			}
		}
	}
}

type funcFilterOut struct{ fclosure }

func (f *funcFilterOut) Arity() int { return 2 }
func (f *funcFilterOut) Eval(w io.Writer, ev *Evaluator) {
	assertArity("filter-out", 2, len(f.args))
	patterns := splitSpacesBytes(ev.Value(f.args[0]))
	texts := splitSpacesBytes(ev.Value(f.args[1]))
	sw := ssvWriter{w: w}
Loop:
	for _, text := range texts {
		for _, pat := range patterns {
			if matchPatternBytes(pat, text) {
				continue Loop
			}
		}
		sw.Write(text)
	}
}

type funcSort struct{ fclosure }

func (f *funcSort) Arity() int { return 1 }
func (f *funcSort) Eval(w io.Writer, ev *Evaluator) {
	assertArity("sort", 1, len(f.args))
	toks := splitSpaces(string(ev.Value(f.args[0])))
	sort.Strings(toks)

	// Remove duplicate words.
	var prev string
	sw := ssvWriter{w: w}
	for _, tok := range toks {
		if prev != tok {
			sw.WriteString(tok)
			prev = tok
		}
	}
}

type funcWord struct{ fclosure }

func (f *funcWord) Arity() int { return 2 }
func (f *funcWord) Eval(w io.Writer, ev *Evaluator) {
	assertArity("word", 2, len(f.args))
	index := numericValueForFunc(ev, f.args[0], "word", "first")
	if index == 0 {
		Error(ev.filename, ev.lineno, `*** first argument to "word" function must be greater than 0.`)
	}
	toks := splitSpacesBytes(ev.Value(f.args[1]))
	if index-1 >= len(toks) {
		return
	}
	w.Write(toks[index-1])
}

type funcWordlist struct{ fclosure }

func (f *funcWordlist) Arity() int { return 3 }
func (f *funcWordlist) Eval(w io.Writer, ev *Evaluator) {
	assertArity("wordlist", 3, len(f.args))
	si := numericValueForFunc(ev, f.args[0], "wordlist", "first")
	if si == 0 {
		Error(ev.filename, ev.lineno, `*** invalid first argument to "wordlist" function: %s`, f.args[0])
	}
	ei := numericValueForFunc(ev, f.args[1], "wordlist", "second")
	if ei == 0 {
		Error(ev.filename, ev.lineno, `*** invalid second argument to "wordlist" function: %s`, f.args[1])
	}

	toks := splitSpacesBytes(ev.Value(f.args[2]))
	if si-1 >= len(toks) {
		return
	}
	if ei-1 >= len(toks) {
		ei = len(toks)
	}

	sw := ssvWriter{w: w}
	for _, t := range toks[si-1 : ei] {
		sw.Write(t)
	}
}

type funcWords struct{ fclosure }

func (f *funcWords) Arity() int { return 1 }
func (f *funcWords) Eval(w io.Writer, ev *Evaluator) {
	assertArity("words", 1, len(f.args))
	toks := splitSpacesBytes(ev.Value(f.args[0]))
	w.Write([]byte(strconv.Itoa(len(toks))))
}

type funcFirstword struct{ fclosure }

func (f *funcFirstword) Arity() int { return 1 }
func (f *funcFirstword) Eval(w io.Writer, ev *Evaluator) {
	assertArity("firstword", 1, len(f.args))
	toks := splitSpacesBytes(ev.Value(f.args[0]))
	if len(toks) == 0 {
		return
	}
	w.Write(toks[0])
}

type funcLastword struct{ fclosure }

func (f *funcLastword) Arity() int { return 1 }
func (f *funcLastword) Eval(w io.Writer, ev *Evaluator) {
	assertArity("lastword", 1, len(f.args))
	toks := splitSpacesBytes(ev.Value(f.args[0]))
	if len(toks) == 0 {
		return
	}
	w.Write(toks[len(toks)-1])
}

// https://www.gnu.org/software/make/manual/html_node/File-Name-Functions.html#File-Name-Functions

type funcJoin struct{ fclosure }

func (f *funcJoin) Arity() int { return 2 }
func (f *funcJoin) Eval(w io.Writer, ev *Evaluator) {
	assertArity("join", 2, len(f.args))
	list1 := splitSpacesBytes(ev.Value(f.args[0]))
	list2 := splitSpacesBytes(ev.Value(f.args[1]))
	sw := ssvWriter{w: w}
	for i, v := range list1 {
		if i < len(list2) {
			sw.Write(v)
			// Use |w| not to append extra ' '.
			w.Write(list2[i])
			continue
		}
		sw.Write(v)
	}
	if len(list2) > len(list1) {
		for _, v := range list2[len(list1):] {
			sw.Write(v)
		}
	}
}

type funcWildcard struct{ fclosure }

func (f *funcWildcard) Arity() int { return 1 }
func (f *funcWildcard) Eval(w io.Writer, ev *Evaluator) {
	assertArity("wildcard", 1, len(f.args))
	sw := ssvWriter{w: w}
	for _, pattern := range splitSpaces(string(ev.Value(f.args[0]))) {
		files, err := filepath.Glob(pattern)
		if err != nil {
			panic(err)
		}
		for _, file := range files {
			sw.WriteString(file)
		}
	}
}

type funcDir struct{ fclosure }

func (f *funcDir) Arity() int { return 1 }
func (f *funcDir) Eval(w io.Writer, ev *Evaluator) {
	assertArity("dir", 1, len(f.args))
	names := splitSpaces(string(ev.Value(f.args[0])))
	sw := ssvWriter{w: w}
	for _, name := range names {
		sw.WriteString(filepath.Dir(name) + string(filepath.Separator))
	}
}

type funcNotdir struct{ fclosure }

func (f *funcNotdir) Arity() int { return 1 }
func (f *funcNotdir) Eval(w io.Writer, ev *Evaluator) {
	assertArity("notdir", 1, len(f.args))
	names := splitSpaces(string(ev.Value(f.args[0])))
	sw := ssvWriter{w: w}
	for _, name := range names {
		if name == string(filepath.Separator) {
			sw.Write([]byte{})
			continue
		}
		sw.WriteString(filepath.Base(name))
	}
}

type funcSuffix struct{ fclosure }

func (f *funcSuffix) Arity() int { return 1 }
func (f *funcSuffix) Eval(w io.Writer, ev *Evaluator) {
	assertArity("suffix", 1, len(f.args))
	toks := splitSpaces(string(ev.Value(f.args[0])))
	sw := ssvWriter{w: w}
	for _, tok := range toks {
		e := filepath.Ext(tok)
		if len(e) > 0 {
			sw.WriteString(e)
		}
	}
}

type funcBasename struct{ fclosure }

func (f *funcBasename) Arity() int { return 1 }
func (f *funcBasename) Eval(w io.Writer, ev *Evaluator) {
	assertArity("basename", 1, len(f.args))
	toks := splitSpaces(string(ev.Value(f.args[0])))
	sw := ssvWriter{w: w}
	for _, tok := range toks {
		e := stripExt(tok)
		sw.WriteString(e)
	}
}

type funcAddsuffix struct{ fclosure }

func (f *funcAddsuffix) Arity() int { return 2 }
func (f *funcAddsuffix) Eval(w io.Writer, ev *Evaluator) {
	assertArity("addsuffix", 2, len(f.args))
	suf := ev.Value(f.args[0])
	toks := splitSpacesBytes(ev.Value(f.args[1]))
	sw := ssvWriter{w: w}
	for _, tok := range toks {
		sw.Write(tok)
		// Use |w| not to append extra ' '.
		w.Write(suf)
	}
}

type funcAddprefix struct{ fclosure }

func (f *funcAddprefix) Arity() int { return 2 }
func (f *funcAddprefix) Eval(w io.Writer, ev *Evaluator) {
	assertArity("addprefix", 2, len(f.args))
	pre := ev.Value(f.args[0])
	toks := splitSpacesBytes(ev.Value(f.args[1]))
	sw := ssvWriter{w: w}
	for _, tok := range toks {
		sw.Write(pre)
		// Use |w| not to append extra ' '.
		w.Write(tok)
	}
}

type funcRealpath struct{ fclosure }

func (f *funcRealpath) Arity() int { return 1 }
func (f *funcRealpath) Eval(w io.Writer, ev *Evaluator) {
	assertArity("realpath", 1, len(f.args))
	names := splitSpaces(string(ev.Value(f.args[0])))
	sw := ssvWriter{w: w}
	for _, name := range names {
		name, err := filepath.Abs(name)
		if err != nil {
			Log("abs: %v", err)
			continue
		}
		name, err = filepath.EvalSymlinks(name)
		if err != nil {
			Log("realpath: %v", err)
			continue
		}
		sw.WriteString(name)
	}
}

type funcAbspath struct{ fclosure }

func (f *funcAbspath) Arity() int { return 1 }
func (f *funcAbspath) Eval(w io.Writer, ev *Evaluator) {
	assertArity("abspath", 1, len(f.args))
	names := splitSpaces(string(ev.Value(f.args[0])))
	sw := ssvWriter{w: w}
	for _, name := range names {
		name, err := filepath.Abs(name)
		if err != nil {
			Log("abs: %v", err)
			continue
		}
		sw.WriteString(name)
	}
}

// http://www.gnu.org/software/make/manual/make.html#Shell-Function
type funcShell struct{ fclosure }

func (f *funcShell) Arity() int { return 1 }

func (f *funcShell) Eval(w io.Writer, ev *Evaluator) {
	assertArity("shell", 1, len(f.args))
	arg := ev.Value(f.args[0])
	cmdline := []string{"/bin/sh", "-c", string(arg)}
	cmd := exec.Cmd{
		Path:   cmdline[0],
		Args:   cmdline,
		Stderr: os.Stderr,
	}
	out, err := cmd.Output()
	if err != nil {
		Log("$(shell %q) failed: %q", arg, err)
	}

	r := string(out)
	r = strings.TrimRight(r, "\n")
	r = strings.Replace(r, "\n", " ", -1)
	fmt.Fprint(w, r)
}

// https://www.gnu.org/software/make/manual/html_node/Call-Function.html#Call-Function
type funcCall struct{ fclosure }

func (f *funcCall) Arity() int { return 0 }

func (f *funcCall) Eval(w io.Writer, ev *Evaluator) {
	variable := string(ev.Value(f.args[0]))
	v := ev.LookupVar(variable)
	Log("call variable %q", v)
	// Evalualte all arguments first before we modify the table.
	var args []Value
	for i, arg := range f.args[1:] {
		args = append(args, tmpval(ev.Value(arg)))
		Log("call $%d: %q=>%q", i+1, arg, args[i])
	}

	var olds []oldVar
	for i, arg := range args {
		name := fmt.Sprintf("%d", i+1)
		olds = append(olds, newOldVar(ev, name))
		ev.outVars.Assign(name,
			SimpleVar{
				value:  arg.String(),
				origin: "automatic", // ??
			})
	}

	var buf bytes.Buffer
	v.Eval(&buf, ev)
	for _, old := range olds {
		old.restore(ev)
	}
	Log("call %q return %q", f.args[0], buf.String())
	w.Write(buf.Bytes())
}

// http://www.gnu.org/software/make/manual/make.html#Foreach-Function
type funcForeach struct{ fclosure }

func (f *funcForeach) Arity() int { return 3 }

func (f *funcForeach) Eval(w io.Writer, ev *Evaluator) {
	assertArity("foreach", 3, len(f.args))
	varname := string(ev.Value(f.args[0]))
	list := ev.Values(f.args[1])
	text := f.args[2]
	old := newOldVar(ev, varname)
	space := false
	for _, word := range list {
		ev.outVars.Assign(varname,
			SimpleVar{
				value:  string(word),
				origin: "automatic",
			})
		if space {
			w.Write([]byte{' '})
		}
		w.Write(ev.Value(text))
		space = true
	}
	old.restore(ev)
}

// TODO(ukai): rewrite new style func.

type fwrapclosure struct {
	fclosure
	name  string
	arity int
	f     func(ev *Evaluator, args []string) string
}

func (f *fwrapclosure) Arity() int {
	return f.arity
}

func (f *fwrapclosure) String() string {
	var args []string
	for _, arg := range f.args {
		args = append(args, arg.String())
	}
	return fmt.Sprintf("${%s %s}", f.name, strings.Join(args, ","))
}

func (f *fwrapclosure) Eval(w io.Writer, ev *Evaluator) {
	var args []string
	for _, arg := range f.args {
		args = append(args, arg.String())
	}
	r := f.f(ev, args)
	fmt.Fprint(w, r)
}

func fwrap(name string, arity int, f func(ev *Evaluator, args []string) string) {
	funcMap[name] = func() Func {
		return &fwrapclosure{
			name:  name,
			arity: arity,
			f:     f,
		}
	}
}

func arity(name string, req int, args []string) []string {
	assertArity(name, req, len(args))
	args[req-1] = strings.Join(args[req-1:], ",")
	return args
}

// http://www.gnu.org/software/make/manual/make.html#Conditional-Functions
func funcIf(ev *Evaluator, args []string) string {
	if len(args) < 2 {
		panic(fmt.Sprintf("*** insufficient number of arguments (%2) to function `if'.", len(args)))
	}
	cond := ev.evalExpr(strings.TrimSpace(args[0]))
	if cond != "" {
		return ev.evalExpr(args[1])
	}
	var results []string
	for _, part := range args[2:] {
		results = append(results, ev.evalExpr(part))
	}
	return strings.Join(results, ",")
}

func funcOr(ev *Evaluator, args []string) string {
	for _, arg := range args {
		cond := ev.evalExpr(strings.TrimSpace(arg))
		if cond != "" {
			return cond
		}
	}
	return ""
}

func funcAnd(ev *Evaluator, args []string) string {
	var cond string
	for _, arg := range args {
		cond = ev.evalExpr(strings.TrimSpace(arg))
		if cond == "" {
			return ""
		}
	}
	return cond
}

// http://www.gnu.org/software/make/manual/make.html#Value-Function
func funcValue(ev *Evaluator, args []string) string {
	args = arity("value", 1, args)
	v := ev.LookupVar(args[0])
	return v.String()
}

// http://www.gnu.org/software/make/manual/make.html#Eval-Function
func funcEval(ev *Evaluator, args []string) string {
	args = arity("eval", 1, args)
	s := ev.evalExpr(args[0])
	if s == "" || (s[0] == '#' && strings.IndexByte(s, '\n') < 0) {
		return ""
	}
	mk, err := ParseMakefileString(s, ev.filename, ev.lineno)
	if err != nil {
		panic(err)
	}

	for _, stmt := range mk.stmts {
		ev.eval(stmt)
	}

	return ""
}

// http://www.gnu.org/software/make/manual/make.html#Origin-Function
func funcOrigin(ev *Evaluator, args []string) string {
	args = arity("origin", 1, args)
	v := ev.LookupVar(args[0])
	return v.Origin()
}

// https://www.gnu.org/software/make/manual/html_node/Flavor-Function.html#Flavor-Function
func funcFlavor(ev *Evaluator, args []string) string {
	args = arity("flavor", 1, args)
	vname := args[0]
	return ev.LookupVar(vname).Flavor()
}

// http://www.gnu.org/software/make/manual/make.html#Make-Control-Functions
func funcInfo(ev *Evaluator, args []string) string {
	args = arity("info", 1, args)
	arg := ev.evalExpr(args[0])
	fmt.Printf("%s\n", arg)
	return ""
}

func funcWarning(ev *Evaluator, args []string) string {
	args = arity("warning", 1, args)
	arg := ev.evalExpr(args[0])
	fmt.Printf("%s:%d: %s\n", ev.filename, ev.lineno, arg)
	return ""
}

func funcError(ev *Evaluator, args []string) string {
	args = arity("error", 1, args)
	arg := ev.evalExpr(args[0])
	Error(ev.filename, ev.lineno, "*** %s.", arg)
	return ""
}
