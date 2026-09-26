# ui stack

- htmx 2.0.11, `static/htmx.min.js`, vendored from
  `https://cdn.jsdelivr.net/npm/htmx.org@2.0.11/dist/htmx.min.js` (sha256
  `d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717`, same
  file on unpkg): the boosted post form. Live updates are a bare
  `EventSource`, no htmx extension.
- `charm.land/fantasy`, already the module's message type (`metaharness/model`
  aliases it): the transcript pages switch on a message's role to render it.
  If `model` ever exposes the roles, ui stops importing fantasy at all —
  a report-back candidate, not a blocker.
