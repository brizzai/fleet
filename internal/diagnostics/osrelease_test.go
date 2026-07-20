package diagnostics

import "testing"

func TestParseOSReleasePrettyName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			"double-quoted (ubuntu)",
			"PRETTY_NAME=\"Ubuntu 24.04.1 LTS\"\nNAME=\"Ubuntu\"\nVERSION_ID=\"24.04\"\n",
			"Ubuntu 24.04.1 LTS",
		},
		{
			// os-release(5) values are shell-compatible: single quotes are
			// just as legal as double.
			"single-quoted (alpine style)",
			"NAME='Alpine Linux'\nPRETTY_NAME='Alpine Linux v3.20'\n",
			"Alpine Linux v3.20",
		},
		{"unquoted", "PRETTY_NAME=Debian\n", "Debian"},
		{"key not first line", "NAME=Fedora\nPRETTY_NAME=\"Fedora Linux 40\"\n", "Fedora Linux 40"},
		{"missing key", "NAME=Void\nID=void\n", ""},
		{"empty input", "", ""},
		{"crlf line endings", "PRETTY_NAME=\"openSUSE Tumbleweed\"\r\nNAME=\"openSUSE\"\r\n", "openSUSE Tumbleweed"},
		{"prefix of other key not matched", "PRETTY_NAME_EXTRA=nope\n", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseOSReleasePrettyName(tt.in); got != tt.want {
				t.Errorf("parseOSReleasePrettyName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
