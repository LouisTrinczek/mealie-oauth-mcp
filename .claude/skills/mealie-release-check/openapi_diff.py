import json
import re
import sys
from collections import defaultdict

METHODS = ("get", "post", "put", "patch", "delete")
MAX_DEPTH = 1
MAX_EXAMPLES = 3


def load(path):
    with open(path) as f:
        return json.load(f)


def operations(spec):
    return {
        (method.upper(), path): op
        for path, item in spec["paths"].items()
        for method, op in item.items()
        if method in METHODS
    }


def flatten(spec, schema, depth=0, seen=()):
    if depth > MAX_DEPTH or not isinstance(schema, dict):
        return set()
    if "$ref" in schema:
        name = schema["$ref"].split("/")[-1]
        if name in seen:
            return set()
        return flatten(spec, spec["components"]["schemas"][name], depth, seen + (name,))
    for key in ("anyOf", "oneOf", "allOf"):
        if key in schema:
            return set().union(*(flatten(spec, s, depth, seen) for s in schema[key]))
    if schema.get("type") == "array":
        return flatten(spec, schema.get("items", {}), depth, seen)
    fields = set()
    for name, sub in schema.get("properties", {}).items():
        fields.add(name)
        fields |= {name + "." + f for f in flatten(spec, sub, depth + 1, seen)}
    return fields


def first_schema(content):
    return next(iter(content.values()), {}).get("schema", {})


def request_fields(spec, op):
    return flatten(spec, first_schema(op.get("requestBody", {}).get("content", {})))


def response_fields(spec, op):
    for code in ("200", "201"):
        content = op.get("responses", {}).get(code, {}).get("content", {})
        if content:
            return flatten(spec, first_schema(content))
    return set()


def parameters(op):
    return {
        p["name"] + " (" + p["in"] + ")": json.dumps(p.get("schema", {}), sort_keys=True)
        for p in op.get("parameters", [])
    }


def enums(spec):
    return {n: s["enum"] for n, s in spec["components"]["schemas"].items() if "enum" in s}


def allowlist_rules(source):
    constants = dict(re.findall(r'(\w+)\s*=\s*"([A-Z,]+)"', source))
    rules = []
    for methods, pattern in re.findall(r'\{\s*(\w+|"[A-Z,]+")\s*,\s*"(/api/[^"]*)"\s*\}', source):
        methods = methods.strip('"') if methods.startswith('"') else constants[methods]
        rules.append((methods.split(","), pattern.strip("/").split("/")))
    reserved = re.search(r"reservedWords = \[\]string\{(.*?)\}", source, re.S).group(1)
    return rules, re.findall(r'"([^"]+)"', reserved)


def reachable(method, path, rules, reserved):
    segments = [("x-param" if s.startswith("{") else s) for s in path.strip("/").split("/")]

    def match(pattern, segment):
        return segment not in reserved if pattern == "{}" else pattern == segment

    return any(
        method in methods and len(pattern) == len(segments) and all(map(match, pattern, segments))
        for methods, pattern in rules
    )


def short(endpoints):
    shown = ", ".join(sorted(endpoints)[:MAX_EXAMPLES])
    rest = len(endpoints) - MAX_EXAMPLES
    return shown + (f", +{rest} more" if rest > 0 else "")


def main(old_path, new_path, allowlist_path):
    old, new = load(old_path), load(new_path)
    with open(allowlist_path) as f:
        rules, reserved = allowlist_rules(f.read())
    old_ops, new_ops = operations(old), operations(new)

    print("== Endpoints added")
    for method, path in sorted(set(new_ops) - set(old_ops)):
        flag = "  <-- REACHABLE through mealie_api, review" if reachable(method, path, rules, reserved) else ""
        print(f"{method} {path}{flag}")

    print("\n== Endpoints removed")
    for method, path in sorted(set(old_ops) - set(new_ops)):
        flag = "  <-- still in the mealie_api allowlist" if reachable(method, path, rules, reserved) else ""
        print(f"{method} {path}{flag}")

    changes = defaultdict(set)
    params = []
    for key in sorted(set(old_ops) & set(new_ops)):
        endpoint = f"{key[0]} {key[1]}"
        for kind, fields in (("request", request_fields), ("response", response_fields)):
            before, after = fields(old, old_ops[key]), fields(new, new_ops[key])
            if kind == "response" and not before:
                continue
            for field in before - after:
                changes[(kind, "-", field)].add(endpoint)
            for field in after - before:
                changes[(kind, "+", field)].add(endpoint)
        before, after = parameters(old_ops[key]), parameters(new_ops[key])
        for name in sorted(set(before) | set(after)):
            if before.get(name) != after.get(name):
                params.append(f"{endpoint}  {name}: {before.get(name, '-')[:120]} -> {after.get(name, '-')[:120]}")

    print("\n== Field changes (request/response, -/+ field, where)")
    for (kind, sign, field), endpoints in sorted(changes.items()):
        print(f"{kind} {sign} {field}  [{short(endpoints)}]")

    print("\n== Query/path parameter changes")
    print("\n".join(params))

    print("\n== Enum changes")
    old_enums, new_enums = enums(old), enums(new)
    for name in sorted(set(old_enums) | set(new_enums)):
        if old_enums.get(name) != new_enums.get(name):
            print(f"{name}: {old_enums.get(name)} -> {new_enums.get(name)}")


if __name__ == "__main__":
    main(*sys.argv[1:4])
