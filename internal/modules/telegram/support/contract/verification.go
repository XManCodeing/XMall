package contract

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	d "github.com/XManCodeing/XMall/internal/modules/telegram/support/domain"
)

// VerificationDay returns the Beijing calendar day for the supplied trusted clock.
func VerificationDay(now time.Time) string {
	return now.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02")
}

// VerificationPolicy enforces the one-hour deadline despite legacy policy values.
// Buffer retention must cover that entire deadline; other limits remain unchanged.
func VerificationPolicy(policy Policy) Policy {
	policy.ChallengeTTL = time.Hour
	if policy.Retention < time.Hour {
		policy.Retention = time.Hour
	}
	return policy
}

// randomChoice returns a cryptographically uniform integer below bound or an entropy error.
func randomChoice(bound int) (int, error) {
	value, err := rand.Int(rand.Reader, big.NewInt(int64(bound)))
	if err != nil {
		return 0, err
	}
	return int(value.Int64()), nil
}

// NewArithmetic fills only the arithmetic fields of challenge using crypto randomness.
// It returns entropy errors without producing a usable partial challenge.
func NewArithmetic(challenge *d.Challenge) error {
	left, err := randomChoice(19)
	if err != nil {
		return err
	}
	left++
	right, err := randomChoice(19)
	if err != nil {
		return err
	}
	right++
	operator, err := randomChoice(4)
	if err != nil {
		return err
	}
	symbols := []string{"+", "-", "*", "/"}
	answer := left + right
	switch operator {
	case 1:
		if left < right {
			left, right = right, left
		}
		answer = left - right
	case 2:
		answer = left * right
	case 3:
		answer = left
		left *= right
	}
	choices := []int{answer}
	for len(choices) < 5 {
		candidate, err := randomChoice(400)
		if err != nil {
			return err
		}
		duplicate := false
		for _, existing := range choices {
			if existing == candidate {
				duplicate = true
			}
		}
		if !duplicate {
			choices = append(choices, candidate)
		}
	}
	for index := len(choices) - 1; index > 0; index-- {
		other, err := randomChoice(index + 1)
		if err != nil {
			return err
		}
		choices[index], choices[other] = choices[other], choices[index]
	}
	labels := make([]string, len(choices))
	for index, choice := range choices {
		labels[index] = strconv.Itoa(choice)
		if choice == answer {
			challenge.AnswerIndex = index
		}
	}
	challenge.Question = fmt.Sprintf("%d %s %d = ?", left, symbols[operator], right)
	challenge.Choices = strings.Join(labels, ",")
	return nil
}

// VerificationAnswer builds the callback suffix for a persisted choice index.
func VerificationAnswer(challenge d.Challenge, index int) string {
	return challenge.Ref + ":" + strconv.Itoa(index)
}

// ParseVerificationAnswer accepts exactly one canonical choice index, never a bare ref.
func ParseVerificationAnswer(value string) (string, int) {
	ref, choice, ok := strings.Cut(value, ":")
	if !ok || ref == "" || len(ref) > 32 || len(choice) != 1 || choice[0] < '0' || choice[0] > '4' {
		return value, -1
	}
	return ref, int(choice[0] - '0')
}
