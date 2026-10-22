package cli

import "testing"

func TestReconcileTemplateIndexSkipsSkippedApproval(t *testing.T) {
	for _, tc := range []struct {
		templates, hashes int
		batch, skipped    bool
		next, need        int
	}{
		{2, 0, false, false, 0, 2}, // approve not yet recorded
		{2, 1, false, false, 1, 2}, // approve confirmed; trade next
		{2, 0, false, true, 1, 1},  // approval skipped; trade is template 1
		{2, 0, true, false, 0, 1},  // atomic batch: one hash
	} {
		next, need := reconcileTemplateIndex(tc.templates, tc.hashes, tc.batch, tc.skipped)
		if next != tc.next || need != tc.need {
			t.Errorf("%+v: got next %d need %d", tc, next, need)
		}
	}
}
