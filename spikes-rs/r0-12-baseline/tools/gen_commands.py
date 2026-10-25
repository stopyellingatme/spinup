#!/usr/bin/env python3
"""Write cli-skel/src/commands.rs: the Go spinup command tree as clap derive stubs.

Usage: tools/gen_commands.py <go spinup binary> > cli-skel/src/commands.rs && cargo fmt --all

It walks `<binary> <path> --help` and copies each command name, its short
description, and its local flags. Group flags (for example `generate --diff`)
become `global = true` arguments of the group. Every leaf also takes free
positional arguments. The output is deterministic for one binary.
"""
import re
import subprocess
import sys

BIN = sys.argv[1]
HEADS = {"Available Commands:", "Develop", "Data", "Ship", "Operate", "Maintain", "Additional Commands:"}
KEYWORDS = {"type", "ref", "self", "use", "mod", "fn", "impl", "move", "match", "async", "await", "dyn", "where", "loop", "box", "override", "final", "in", "for", "if", "else", "let", "static", "const", "crate", "super", "struct", "enum", "trait", "true", "false", "return", "break", "continue", "extern", "pub", "unsafe", "while", "as", "yield", "try", "macro", "priv", "virtual", "abstract", "become", "do", "typeof", "unsized", "gen"}
FLAG = re.compile(r"^\s+(?:-([a-zA-Z]), )?--([a-z0-9][a-z0-9-]*)(?: ([a-zA-Z]+))?\s{2,}(.*)$")


def help_of(path):
    return subprocess.run([BIN, *path, "--help"], capture_output=True, text=True, check=True).stdout


def parse(path):
    text = help_of(path)
    lines = text.splitlines()
    para = []
    for line in lines:
        if not line.strip():
            break
        para.append(line.strip())
    about = " ".join(para)
    subs, flags, section = [], [], None
    for line in lines:
        s = line.strip()
        if s in HEADS:
            section = "cmds"
            continue
        if s == "Flags:":
            section = "flags"
            continue
        if s.endswith(":") and not line.startswith(" "):
            section = None
            continue
        if not s:
            if section == "cmds":
                section = None
            continue
        if section == "cmds":
            m = re.match(r"^  ([a-z][\w-]*)\s", line)
            if m and m.group(1) != "help":
                subs.append(m.group(1))
        elif section == "flags":
            m = FLAG.match(line)
            if m and m.group(2) != "help":
                flags.append((m.group(1), m.group(2), m.group(3), m.group(4).strip()))
    return {"name": path[-1] if path else "spinup", "about": about, "flags": flags,
            "children": [parse(path + [c]) for c in subs], "path": path}


def camel(name):
    return "".join(p.capitalize() for p in re.split(r"[-_]", name))


def ident(name):
    i = name.replace("-", "_")
    return f"r#{i}" if i in KEYWORDS else i


def lit(s):
    return '"' + s.replace("\\", "\\\\").replace('"', '\\"') + '"'


out = []
counts = {"leaf": 0, "group": 0, "flags": 0}


def emit_args(node, taken, global_flags):
    """Field lines for the flags of node that no ancestor defines."""
    fields = []
    for short, long, typ, desc in node["flags"]:
        if long in taken:
            continue
        taken = taken | {long}
        counts["flags"] += 1
        attr = [f"long = {lit(long)}"]
        if short and short not in ("h", "V") and f"-{short}" not in taken:
            attr.append(f"short = '{short}'")
            taken = taken | {f"-{short}"}
        if global_flags:
            attr.append("global = true")
        attr.append(f"help = {lit(desc)}")
        if typ is None:
            ty = "bool"
        elif typ.endswith("Array") or typ == "strings":
            ty = "Vec<String>"
        else:
            attr.append(f"value_name = {lit(typ.upper())}")
            ty = "Option<String>"
        fields.append(f"    #[arg({', '.join(attr)})]\n    pub {ident(long)}: {ty},")
    return fields, taken


def emit(node, taken, prefix):
    """Writes the args struct (and the subcommand enum) of node. Returns its type name."""
    name = prefix + camel(node["name"])
    if node["path"] == []:
        name = "Cli"
    fields, taken2 = emit_args(node, taken, global_flags=bool(node["children"]))
    lines = []
    if node["children"]:
        counts["group"] += 1
        enum = name + "Command"
        variants = []
        for c in node["children"]:
            cname = emit(c, taken2, name if name != "Cli" else "")
            variants.append(f"    #[command(about = {lit(c['about'])})]\n    {camel(c['name'])}({cname}),")
        out.append("#[derive(Debug, Subcommand)]\npub enum " + enum + " {\n" + "\n".join(variants) + "\n}\n")
        fields.append(f"    #[command(subcommand)]\n    pub command: Option<{enum}>,")
        if node["path"]:
            fields.append("    /// Free arguments of a runnable group.\n    pub args: Vec<String>,")
    else:
        counts["leaf"] += 1
        fields.append("    /// Free arguments (names, fields, and versions).\n    pub args: Vec<String>,")
    head = "#[derive(Debug, Args)]\n"
    if node["children"] and node["path"]:
        head += "#[command(args_conflicts_with_subcommands = true)]\n"
    if node["path"] == []:
        head = "#[derive(Debug, Parser)]\n"
        head += '#[command(name = "spinup", version, about = ' + lit(node["about"]) + ")]\n"
    lines.append(head + f"pub struct {name} {{\n" + "\n".join(fields) + "\n}\n")
    out.extend(lines)
    return name


def emit_match(node, prefix, var, mode):
    """A match that maps a parsed command to its path string (mode "path")
    or to its free arguments (mode "args")."""
    name = prefix + camel(node["name"]) if node["path"] else "Cli"
    if not node["children"]:
        return lit(" ".join(node["path"])) if mode == "path" else f"&{var}.args"
    enum = name + "Command"
    arms = []
    for c in node["children"]:
        inner = emit_match(c, name if name != "Cli" else "", "c", mode)
        bind = "c" if c["children"] or mode == "args" else "_"
        arms.append(f"            Some({enum}::{camel(c['name'])}({bind})) => {inner},")
    if mode == "path":
        none = lit(" ".join(node["path"])) if node["path"] else '""'
    else:
        none = f"&{var}.args" if node["path"] else "&[]"
    return "match &" + var + ".command {\n" + "\n".join(arms) + f"\n            None => {none},\n        }}"


tree = parse([])
emit(tree, set(), "")
body = emit_match(tree, "", "self", "path")
args = emit_match(tree, "", "self", "args")
print("// @generated by tools/gen_commands.py from the Go spinup --help tree. Do not edit.")
print(f"// {counts['leaf']} leaf commands, {counts['group']} groups (with the root), {counts['flags']} flags.")
print("#![allow(clippy::struct_excessive_bools, clippy::large_enum_variant)]\n")
print("use clap::{Args, Parser, Subcommand};\n")
print("\n".join(out))
print(f"/// The number of leaf commands.\npub const LEAVES: usize = {counts['leaf']};\n")
print("impl Cli {\n    /// The command path, for example `db migrate`. The root is the empty string.\n    pub fn path(&self) -> &'static str {\n        " + body + "\n    }\n\n    /// The free arguments of the command.\n    pub fn args(&self) -> &[String] {\n        " + args + "\n    }\n}")
