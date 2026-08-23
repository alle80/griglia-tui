package domain

import (
	"strings"
	"time"
)

const MaxQuestionTextLength = 2000

type Question struct {
	ID             int64
	TaskID         int64
	Body           string
	Blocking       bool
	AskedBy        AgentIdentity
	AskedAt        time.Time
	Answer         *string
	AnsweredAt     *time.Time
	AcknowledgedAt *time.Time
}

func (q Question) Answered() bool     { return q.AnsweredAt != nil }
func (q Question) Acknowledged() bool { return q.AcknowledgedAt != nil }

func ValidateQuestionText(text string) error {
	if strings.TrimSpace(text) == "" || len([]rune(text)) > MaxQuestionTextLength {
		return ErrInvalid
	}
	return nil
}

type QuestionFilter string

const (
	QuestionsAll            QuestionFilter = "all"
	QuestionsUnanswered     QuestionFilter = "unanswered"
	QuestionsUnacknowledged QuestionFilter = "unacknowledged"
)
