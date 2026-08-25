package scankit

import (
	"reflect"
	"testing"
)

const duplicateRegexCompilePattern = `[a-z]{1,8}@[a-z]{1,8}\.(com|net)`

func TestCompileReusesIdenticalByteRegexIR(t *testing.T) {
	database, err := Compile([]Expression{
		{Id: 1, Pattern: duplicateRegexCompilePattern},
		{Id: 2, Pattern: duplicateRegexCompilePattern, Flags: CompileQuiet},
		{Id: 3, Pattern: duplicateRegexCompilePattern, Flags: CompileSingleMatch},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(database.regexPrograms) != 3 {
		t.Fatalf("regex program count = %d, want 3", len(database.regexPrograms))
	}
	first, second, third := database.regexPrograms[0], database.regexPrograms[1], database.regexPrograms[2]
	if &first.program.states[0] != &second.program.states[0] || &first.program.states[0] != &third.program.states[0] {
		t.Fatal("identical expressions did not share NFA states")
	}
	if first.program.verifierDFA == nil || first.program.verifierDFA != second.program.verifierDFA || first.program.verifierDFA != third.program.verifierDFA {
		t.Fatal("identical expressions did not share verifier DFA")
	}

	got, err := database.Scan([]byte("a@b.com c@d.net"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Match{
		{Id: 1, From: 0, To: 7},
		{Id: 3, From: 0, To: 7},
		{Id: 1, From: 8, To: 15},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Scan() = %#v, want %#v", got, want)
	}
}

func TestCompileDoesNotShareDifferentByteRegexLanguages(t *testing.T) {
	database, err := Compile([]Expression{
		{Id: 1, Pattern: `.`},
		{Id: 2, Pattern: `.`, Flags: CompileDotAll},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(database.regexPrograms) != 2 {
		t.Fatalf("regex program count = %d, want 2", len(database.regexPrograms))
	}
	if &database.regexPrograms[0].program.states[0] == &database.regexPrograms[1].program.states[0] {
		t.Fatal("different byte languages unexpectedly shared NFA states")
	}
	got, err := database.Scan([]byte("\n"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []Match{{Id: 2, From: 0, To: 1}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Scan() = %#v, want %#v", got, want)
	}
}

func FuzzCompileReusedByteRegexIRMatchesPerExpressionEvents(f *testing.F) {
	f.Add([]byte("a@b.com c@d.net"), uint8(3))
	f.Add([]byte("invalid@domain.invalid x@y.net"), uint8(8))
	f.Fuzz(func(t *testing.T, data []byte, count uint8) {
		if len(data) > 4_096 {
			t.Skip()
		}
		count = count%8 + 1
		expressions := make([]Expression, count)
		for index := range expressions {
			expressions[index] = Expression{Id: uint32(index + 1), Pattern: duplicateRegexCompilePattern}
		}
		database, err := Compile(expressions)
		if err != nil {
			t.Fatal(err)
		}
		matches, err := database.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		byID := make([][]Match, count)
		for _, match := range matches {
			byID[match.Id-1] = append(byID[match.Id-1], Match{From: match.From, To: match.To})
		}
		for index := 1; index < len(byID); index++ {
			if !reflect.DeepEqual(byID[index], byID[0]) {
				t.Fatalf("events for rule %d = %#v, want %#v", index+1, byID[index], byID[0])
			}
		}
	})
}
