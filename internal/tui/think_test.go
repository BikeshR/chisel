package tui

import "testing"

func TestRenderAssistantText(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		show bool
		want string
	}{
		{
			name: "complete think block collapsed",
			raw:  "<think>\nreasoning here\n</think>\nHello! How can I help?",
			show: false,
			want: dimStyle.Render("⌄ thinking (/think to show)") + "\nHello! How can I help?",
		},
		{
			name: "complete think block shown in full",
			raw:  "<think>\nreasoning here\n</think>\nHello! How can I help?",
			show: true,
			want: "<think>\nreasoning here\n</think>\nHello! How can I help?",
		},
		{
			name: "still-streaming think block with no close tag yet",
			raw:  "<think>\nstill going",
			show: false,
			want: dimStyle.Render("⌄ thinking… (/think to show)"),
		},
		{
			name: "no think tags at all passes through unchanged",
			raw:  "just a plain answer",
			show: false,
			want: "just a plain answer",
		},
		{
			name: "partial split opening tag not yet a match",
			raw:  "<thi",
			show: false,
			want: "<thi",
		},
		{
			name: "text before and after a think block both preserved",
			raw:  "before <think>hidden</think> after",
			show: false,
			want: "before " + dimStyle.Render("⌄ thinking (/think to show)") + " after",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := renderAssistantText(c.raw, c.show); got != c.want {
				t.Errorf("renderAssistantText(%q, %v) =\n  %q\nwant\n  %q", c.raw, c.show, got, c.want)
			}
		})
	}
}
