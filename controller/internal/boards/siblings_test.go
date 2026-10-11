package boards

import (
	"os"
	"reflect"
	"regexp"
	"testing"

	"github.com/skitzo2000/espdns/controller/internal/memplan"
)

// Sibling images pair both ways, with the same chip, clocks and memory defaults, and the
// firmware has the same pairs (firmware/CMakeLists.txt): a node takes a sibling's releases
// only if both sides agree it is one.
func TestSiblings(t *testing.T) {
	cmake, err := os.ReadFile("../../../firmware/CMakeLists.txt")
	if err != nil {
		t.Fatal(err)
	}
	fw := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^set\(sibling_(\S+) (\S+)\)$`).FindAllStringSubmatch(string(cmake), -1) {
		fw[m[1]] = m[2]
	}
	if !reflect.DeepEqual(fw, Siblings) {
		t.Errorf("firmware/CMakeLists.txt pairs %v, Siblings %v", fw, Siblings)
	}
	target := regexp.MustCompile(`(?m)^CONFIG_IDF_TARGET="(.*)"$`)
	for a, b := range Siblings {
		if Siblings[b] != a {
			t.Errorf("%s pairs with %s, %s with %s", a, b, b, Siblings[b])
		}
		if !reflect.DeepEqual(Images[a], Images[b]) || !reflect.DeepEqual(Clocks[a], Clocks[b]) ||
			!reflect.DeepEqual(memplan.Values(memplan.Keys{}, a, 8192), memplan.Values(memplan.Keys{}, b, 8192)) {
			t.Errorf("%s and %s differ in their chip, clocks or memory defaults", a, b)
		}
		var targets [2]string
		for i, img := range []string{a, b} {
			d, err := os.ReadFile("../../../firmware/images/" + img + "/sdkconfig.defaults")
			if err != nil {
				t.Fatal(err)
			}
			if m := target.FindSubmatch(d); m != nil {
				targets[i] = string(m[1])
			}
		}
		if targets[0] == "" || targets[0] != targets[1] {
			t.Errorf("%s and %s build for %q and %q", a, b, targets[0], targets[1])
		}
	}
	if !Swappable("esp32s3-octal", "esp32s3-eth") || !Swappable("esp32s3-eth", "esp32s3-eth") ||
		Swappable("esp32s3-octal", "esp32s3-quad") || Swappable("", "") {
		t.Error("Swappable")
	}
}
