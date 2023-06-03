package logger

import (
	"fmt"
	"os"
	"strings"

	"github.com/fatih/color"
)

// LogStatus represents the type of log level being emitted,
// however it's important to note that each level here is not
// discretely toggleable. For example, VERBOSE and DEBUG are
// distinct tiers, however SUCCESS, NEW, REMOVE, and STOP are
// all same tier and are 'important' logs. See the LogLevel
// enum for the tiers and how each status maps to a level.
type LogStatus int

const (
	// Verbose
	VERBOSE LogStatus = iota

	// Debug
	DEBUG

	// Info
	INFO

	// Important
	SUCCESS
	NEW
	REMOVE
	STOP

	// Warning
	WARNING

	// Error
	ERROR
	FATAL
)

const DEFAULT_MIN_STATUS = info

func getMinLogLevelFromEnv() LogLevel {
	if value, ok := os.LookupEnv("THEA_LOG_LEVEL"); ok {
		switch strings.ToLower(value) {
		case "verbose":
			return verbose
		case "debug":
			return debug
		case "info":
			return info
		case "important":
			return important
		case "warning":
			return warning
		case "error":
			return err
		default:
			fmt.Printf("ERR: logging level %v is not recognized, falling back to default\n", value)
		}
	}

	return DEFAULT_MIN_STATUS
}

type LogLevel int

const (
	verbose LogLevel = iota
	debug
	info
	important
	warning
	err
)

// Level returns the mapping between a LogStatus - used to describe the intent
// of a log level - and it's LogLevel, which is a tiered set of enums that describe
// the 'importance' of the message to the user. E.g. a 'FATAL' status error is mapped
// to the most important level: err.
func (e LogStatus) Level() LogLevel {
	switch e {
	case VERBOSE:
		return verbose
	case DEBUG:
		return debug
	case INFO:
		return info
	case SUCCESS:
		fallthrough
	case NEW:
		fallthrough
	case REMOVE:
		fallthrough
	case STOP:
		return important
	case WARNING:
		return warning
	case ERROR:
		fallthrough
	case FATAL:
		fallthrough
	default:
		return err
	}
}

func (e LogStatus) String() string {
	return []string{
		"V",
		"D",
		"I",
		"✓",
		"+",
		"-",
		"X",
		"!",
		"!!",
		"PANIC",
	}[e]
}

func (e LogStatus) Color() *color.Color {
	return []*color.Color{
		color.New(color.FgWhite, color.Italic),                //Verbose
		color.New(color.FgWhite, color.Italic),                //Debug
		color.New(color.FgWhite),                              //Info
		color.New(color.FgHiGreen),                            //Success
		color.New(color.FgGreen, color.Italic),                //New
		color.New(color.FgYellow, color.Italic),               //Remove
		color.New(color.FgHiYellow),                           //Stop
		color.New(color.FgYellow, color.Underline),            //Warning
		color.New(color.FgHiRed, color.Bold),                  //Error
		color.New(color.FgHiRed, color.Bold, color.Underline), //PANIC
	}[e]
}

type Logger interface {
	Emit(LogStatus, string, ...interface{})
}

type loggerImpl struct {
	name string
}

func (l *loggerImpl) Emit(status LogStatus, message string, interpolations ...interface{}) {
	Log.Emit(status, l.name, message, interpolations...)
}

type LoggerManager interface {
	GetLogger(string) Logger
	Emit(LogStatus, string, string, ...interface{})
}

var Log LoggerManager = &loggerMgr{
	offset:   0,
	minLevel: getMinLogLevelFromEnv(),
}

type loggerMgr struct {
	offset   int
	minLevel LogLevel
}

func (l *loggerMgr) GetLogger(name string) Logger {
	return &loggerImpl{name: name}
}

func (l *loggerMgr) Emit(status LogStatus, name string, message string, interpolations ...interface{}) {
	if status.Level() < l.minLevel {
		return
	}

	l.setNameOffset(len(name))
	padding := strings.Repeat(" ", l.offset-len(name))
	msg := fmt.Sprintf("[%s] %s(%s) %s", name, padding, status, fmt.Sprintf(message, interpolations...))

	status.Color().Print(msg)
}

func (l *loggerMgr) setNameOffset(offset int) {
	if offset > l.offset {
		l.offset = offset
	}
}

func Get(name string) Logger {
	return Log.GetLogger(name)
}