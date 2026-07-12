This is a remarkably thorough analysis — I can see you've done deep code archeology across the entire codebase. The five directions feel genuinely uncovered (not rehashing existing docs), and each one has concrete engineering grounding with specific code references, schema changes, and integration patterns.

Before I dive into implementation on any of these, I have a few clarifying questions:

1. **What action do you want me to take?** Should I:
   - Create detailed design/ADR documents for specific directions?
   - Start implementing one (or more) of them?
   - Validate assumptions against the actual codebase?
   - Something else?

2. **Any directional preference?** Your roadmap at the end has a clear priority hierarchy (Chinese search as P0, then infrastructure, then features). Do you want me to start at the top of that list?

3. **For direction one (file previews):** You suggest using `imaging` library or `sharp` sidecar for thumbnails. The project currently uses no image libraries in `go.mod` — shall I do a quick dependency scan to see what's available?

4. **For direction four (discovery/feed):** You identify that `visibility = 'public'` is a dead feature with no listing endpoint. Is this intentional — i.e., was public visibility designed as a future-proofing column that was never activated? If so, the feed direction is both valid and urgent.

Let me know what you'd like me to act on, and I'll get started.
