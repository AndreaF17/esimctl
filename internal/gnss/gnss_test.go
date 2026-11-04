package gnss

import (
	"fmt"
	"testing"
)

func sentence(body string) string {
	var sum byte
	for i := 0; i < len(body); i++ {
		sum ^= body[i]
	}
	return fmt.Sprintf("$%s*%02X", body, sum)
}

func TestParseFix(t *testing.T) {
	var fix Fix
	for _, s := range []string{
		sentence("GPGGA,123519.00,4528.0662,N,00911.4204,E,1,09,0.9,121.4,M,46.9,M,,"),
		sentence("GNGSA,A,3,04,05,09,12,24,25,29,31,,,,,1.8,0.9,1.5"),
		sentence("GPRMC,123519.00,A,4528.0662,N,00911.4204,E,10.0,84.4,041126,,,A"),
		sentence("GPGSV,3,1,11,04,40,083,46,05,17,308,41,09,21,047,38,12,77,248,43"),
		sentence("GLGSV,2,1,06,65,30,100,35,66,45,200,40,72,10,300,20,73,50,050,44"),
	} {
		if Parse(&fix, s) == "" {
			t.Fatalf("rejected %s", s)
		}
	}
	if !fix.Valid || fix.Mode != "3d" || fix.SatsUsed != 9 || fix.HDOP != 0.9 || fix.Alt != 121.4 {
		t.Fatalf("fix %+v", fix)
	}
	if fix.Lat != 45.46777 || fix.Lon != 9.19034 {
		t.Fatalf("coords %v %v", fix.Lat, fix.Lon)
	}
	if fix.SpeedKmh != 18.5 || fix.Course != 84.4 {
		t.Fatalf("speed %v course %v", fix.SpeedKmh, fix.Course)
	}
	if fix.Time == nil || fix.Time.Format("2006-01-02 15:04:05") != "2026-11-04 12:35:19" {
		t.Fatalf("time %v", fix.Time)
	}
	if fix.inView["GP"] != 11 || fix.inView["GL"] != 6 {
		t.Fatalf("in view %v", fix.inView)
	}
}

func TestParseNoFix(t *testing.T) {
	var fix Fix
	Parse(&fix, sentence("GPGGA,,,,,,0,00,99.99,,,,,,"))
	Parse(&fix, sentence("GPRMC,,V,,,,,,,,,,N"))
	if fix.Valid || fix.Mode != "none" {
		t.Fatalf("fix %+v", fix)
	}
}

func TestChecksum(t *testing.T) {
	var fix Fix
	if Parse(&fix, "$GPRMC,123519.00,A,4528.0662,N,00911.4204,E,10.0,84.4,041126,,,A*00") != "" {
		t.Fatal("accepted a bad checksum")
	}
	if fix.Valid {
		t.Fatal("bad sentence changed the fix")
	}
	if coord("00911.4204", "W") != -9.19034 || coord("4528.0662", "S") != -45.46777 {
		t.Fatal("hemisphere sign")
	}
}

func TestParseLoc(t *testing.T) {
	fix := Fix{Mode: "none", SatsInView: 7}
	if err := ParseLoc(&fix, "123519.000,45.46777,9.19034,0.9,121.4,3,084.24,18.5,10.0,041126,09"); err != nil {
		t.Fatal(err)
	}
	if !fix.Valid || fix.Mode != "3d" || fix.Lat != 45.46777 || fix.Lon != 9.19034 || fix.SatsUsed != 9 || fix.SatsInView != 9 {
		t.Fatalf("fix %+v", fix)
	}
	if fix.Course != 84.4 || fix.SpeedKmh != 18.5 || fix.Alt != 121.4 {
		t.Fatalf("course %v speed %v alt %v", fix.Course, fix.SpeedKmh, fix.Alt)
	}
	if fix.Time.Format("2006-01-02 15:04:05") != "2026-11-04 12:35:19" {
		t.Fatalf("time %v", fix.Time)
	}
	if ParseLoc(&fix, "garbage") == nil {
		t.Fatal("accepted garbage")
	}
}
