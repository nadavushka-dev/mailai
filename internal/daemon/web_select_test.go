package daemon

import (
	"strings"
	"testing"
)

// SPEC §6/§12a (decided 2026-11-24, replacing the "Delete read" button):
// Gmail-style selection in the web Mail lists. Every thread row has a
// checkbox (click toggles, shift+click a range, x the focused row), the
// list header selects every row shown, and while anything is selected it
// says how many with Delete and Clear. Delete asks once with the count,
// then deletes each thread with the person's per-thread delete.
func TestWeb_SelectThreadsAndDeleteThem(t *testing.T) {
	page := webPage(t)
	for _, gone := range []string{"Delete read", "delete-read-agents", "bulk-del", "read_between_agents"} {
		if strings.Contains(page, gone) {
			t.Fatalf("page still has %s", gone)
		}
	}
	for _, need := range []string{
		// The checkbox on a thread row, and the header's select-all.
		`<span class="pick" role="checkbox" aria-checked="${picked.has(t.thread_id)}" data-pick="${esc(t.thread_id)}"`,
		`class="pick all" role="checkbox" aria-checked="${all ? 'true' : picked.size ? 'mixed' : 'false'}" data-pick-all`,
		"${picked.size} selected",
		`data-a="delete-picked"`,
		`data-a="clear-picked"`,
		// Click, shift+click a range from the last clicked row, x, Esc.
		"if (e.shiftKey && pickAnchor",
		"if (e.key === 'x' && pickable())",
		"if (picked.size) { clearPicked(); return; }",
		// Asks once with the count; the person's delete, a thread at a time.
		"if (!confirm(`Delete ${n} thread${n === 1 ? '' : 's'}",
		"await api('/api/delete', {thread: id})",
		// Selection follows the list: threads no longer shown are dropped.
		"for (const id of [...picked]) if (!shown.has(id)) picked.delete(id);",
	} {
		if !strings.Contains(page, need) {
			t.Fatalf("page missing %s", need)
		}
	}
}
