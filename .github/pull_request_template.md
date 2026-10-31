## Summary

<!-- What changes, and why. Link the issue it closes, for example "Closes #123". -->

## Checklist

- [ ] Commit subjects follow [Conventional Commits](https://www.conventionalcommits.org) and every commit is signed off (`git commit -s`).
- [ ] I added tests, and `make cover` passes.
- [ ] I reviewed the golden file diffs. For changes to `internal/mcpserver/testdata/tool_surface.golden.json`, I checked them against the [tool stability policy](https://github.com/LouisTrinczek/mealie-oauth-mcp/blob/main/CONTRIBUTING.md#tool-stability-policy).
- [ ] I updated the README and [CONTRIBUTING.md](https://github.com/LouisTrinczek/mealie-oauth-mcp/blob/main/CONTRIBUTING.md) where they are affected.
- [ ] Nothing in logs, errors, or tool output exposes tokens, auth codes, or other secrets.
