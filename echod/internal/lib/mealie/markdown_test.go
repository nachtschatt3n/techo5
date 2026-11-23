package mealie

import "testing"

func TestPlainMarkdown(t *testing.T) {
	for in, want := range map[string]string{
		"Fry the **onion** and _garlic_.":                   "Fry the onion and garlic.",
		"See [the video](https://example.com/v?a=1) first.": "See the video first.",
		"![photo](x.jpg) Done":                              "photo Done",
		"# Sauce\nStir *well*.":                             "Sauce\nStir well.",
		"- one\n- two\n3. three":                            "one\ntwo\nthree",
		"Use snake_case_names and 2 * 3":                    "Use snake_case_names and 2 * 3",
		"a\r\n\r\n\r\n\r\nb":                                "a\n\nb",
		"  spaced   out  ":                                  "spaced out",
		"`code` and ~~gone~~ and <b>tag</b>":                "code and gone and tag",
		"Bake at 180 °C for 10 min.":                        "Bake at 180 °C for 10 min.",
		"":                                                  "",
		"__bold__ text":                                     "bold text",
	} {
		if got := Plain(in); got != want {
			t.Errorf("Plain(%q) = %q, want %q", in, got, want)
		}
	}
}
