package contract

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
)

// TestArithmeticChoices checks all operators, exact division and one unique correct choice.
func TestArithmeticChoices(t *testing.T) {
	seen := map[string]bool{}
	for range 300 {
		var challenge d.Challenge
		if err := NewArithmetic(&challenge); err != nil {
			t.Fatal(err)
		}
		var left, right int
		var operator string
		if _, err := fmt.Sscanf(challenge.Question, "%d %s %d = ?", &left, &operator, &right); err != nil {
			t.Fatal(err)
		}
		seen[operator] = true
		answer := left + right
		switch operator {
		case "-":
			answer = left - right
		case "*":
			answer = left * right
		case "/":
			if right == 0 || left%right != 0 {
				t.Fatal(challenge.Question)
			}
			answer = left / right
		}
		labels := strings.Split(challenge.Choices, ",")
		unique := map[string]bool{}
		correct := 0
		for _, label := range labels {
			if unique[label] {
				t.Fatal("duplicate choice")
			}
			unique[label] = true
			if label == strconv.Itoa(answer) {
				correct++
			}
		}
		if len(labels) != 5 || correct != 1 || labels[challenge.AnswerIndex] != strconv.Itoa(answer) {
			t.Fatal(challenge)
		}
	}
	if len(seen) != 4 {
		t.Fatal("missing operators", seen)
	}
}
