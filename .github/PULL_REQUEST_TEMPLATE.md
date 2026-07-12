**What & why**

**Checklist**
- [ ] `make check` passes (gofmt, vet, tests)
- [ ] New behavior has tests; bug fixes have a regression test
- [ ] Touched adapters/delivery/readiness? Ran `make demo` (and a live
      harness smoke test if you have one installed)
- [ ] Docs updated where behavior changed (README, docs/, `/help`)
- [ ] Honest-capability rule respected (no claimed features an adapter
      can't deliver)
