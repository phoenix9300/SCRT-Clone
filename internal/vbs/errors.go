package vbs

import (
	"errors"
	"fmt"
	"strings"
)

// Error is returned for syntax and runtime errors raised by a script.
//
// Number carries the VBScript error number when it is known (13 type
// mismatch, 9 subscript out of range, 11 division by zero, 424 object
// required, 438 unknown member, 1002.. syntax errors, ...). Errors returned by
// host objects that are not *Error values are wrapped with HostErrorNumber.
type Error struct {
	Number  int    // VBScript error number
	Message string // description (Err.Description)
	File    string // script file name passed to Run
	Line    int    // 1-based line, 0 when unknown
	Col     int    // 1-based column, 0 when unknown
	Syntax  bool   // true for compilation (lexer/parser) errors
	Source  string // Err.Source

	// bare is set when Message is the plain standard text for Number, which
	// lets the evaluator append the offending name ("Type mismatch: 'CInt'").
	bare bool
}

// HostErrorNumber is the Err.Number reported for errors returned by host
// objects and host functions that are not *Error values.
const HostErrorNumber = 1000

// ErrQuit is returned by Run and Call when the script stopped itself with
// WScript.Quit. It is not a failure; the exit code passed to Quit is
// available through Interpreter.ExitCode. Host objects may also return
// ErrQuit from a method to stop the running script; it cannot be trapped by
// On Error Resume Next.
var ErrQuit = errors.New("vbs: script terminated by WScript.Quit")

// Error implements the error interface.
func (e *Error) Error() string {
	var sb strings.Builder
	sb.WriteString(e.File)
	if e.Line > 0 {
		fmt.Fprintf(&sb, "(%d, %d)", e.Line, e.Col)
	}
	if sb.Len() > 0 {
		sb.WriteString(": ")
	}
	kind := "runtime"
	if e.Syntax {
		kind = "compilation"
	}
	fmt.Fprintf(&sb, "VBScript %s error %d: %s", kind, e.Number, e.Message)
	return sb.String()
}

// NewError creates a runtime error with the given VBScript error number and
// description. Host objects can return it from Get/Set to raise a specific
// error that scripts can inspect through Err.Number / Err.Description.
func NewError(number int, message string) *Error {
	if message == "" {
		message = errText(number)
	}
	return &Error{Number: number, Message: message}
}

var errMessages = map[int]string{
	5:     "Invalid procedure call or argument",
	6:     "Overflow",
	7:     "Out of memory",
	9:     "Subscript out of range",
	10:    "This array is fixed or temporarily locked",
	11:    "Division by zero",
	13:    "Type mismatch",
	14:    "Out of string space",
	17:    "Can't perform requested operation",
	28:    "Out of stack space",
	35:    "Sub or Function not defined",
	48:    "Error in loading DLL",
	51:    "Internal error",
	52:    "Bad file name or number",
	53:    "File not found",
	54:    "Bad file mode",
	55:    "File already open",
	57:    "Device I/O error",
	58:    "File already exists",
	61:    "Disk full",
	62:    "Input past end of file",
	67:    "Too many files",
	68:    "Device unavailable",
	70:    "Permission denied",
	71:    "Disk not ready",
	74:    "Can't rename with different drive",
	75:    "Path/File access error",
	76:    "Path not found",
	91:    "Object variable not set",
	92:    "For loop not initialized",
	94:    "Invalid use of Null",
	322:   "Can't create necessary temporary file",
	424:   "Object required",
	429:   "ActiveX component can't create object",
	430:   "Class doesn't support Automation",
	432:   "File name or class name not found during Automation operation",
	438:   "Object doesn't support this property or method",
	440:   "Automation error",
	445:   "Object doesn't support this action",
	446:   "Object doesn't support named arguments",
	447:   "Object doesn't support current locale setting",
	448:   "Named argument not found",
	449:   "Argument not optional",
	450:   "Wrong number of arguments or invalid property assignment",
	451:   "Object not a collection",
	453:   "Specified DLL function not found",
	457:   "This key is already associated with an element of this collection",
	458:   "Variable uses an Automation type not supported in VBScript",
	462:   "The remote server machine does not exist or is unavailable",
	500:   "Variable is undefined",
	501:   "Illegal assignment",
	502:   "Object not safe for scripting",
	505:   "Invalid or unqualified reference",
	506:   "Class not defined",
	507:   "An exception occurred",
	1001:  "Out of memory",
	1002:  "Syntax error",
	1003:  "Expected ':'",
	1005:  "Expected '('",
	1006:  "Expected ')'",
	1007:  "Expected ']'",
	1010:  "Expected identifier",
	1011:  "Expected '='",
	1012:  "Expected 'If'",
	1013:  "Expected 'To'",
	1014:  "Expected 'End'",
	1015:  "Expected 'Function'",
	1016:  "Expected 'Sub'",
	1017:  "Expected 'Then'",
	1018:  "Expected 'Wend'",
	1019:  "Expected 'Loop'",
	1020:  "Expected 'Next'",
	1021:  "Expected 'Case'",
	1022:  "Expected 'Select'",
	1023:  "Expected expression",
	1024:  "Expected statement",
	1025:  "Expected end of statement",
	1026:  "Expected integer constant",
	1027:  "Expected 'While' or 'Until'",
	1028:  "Expected 'While', 'Until' or end of statement",
	1029:  "Expected 'With'",
	1030:  "Identifier too long",
	1031:  "Invalid number",
	1032:  "Invalid character",
	1033:  "Unterminated string constant",
	1034:  "Unterminated comment",
	1037:  "Invalid use of 'Me' keyword",
	1038:  "'loop' without 'do'",
	1039:  "Invalid 'exit' statement",
	1040:  "Invalid 'for' loop control variable",
	1041:  "Name redefined",
	1042:  "Must be first statement on the line",
	1043:  "Cannot assign to non-ByVal argument",
	1044:  "Cannot use parentheses when calling a Sub",
	1045:  "Expected literal constant",
	1046:  "Expected 'In'",
	1047:  "Expected 'Class'",
	1048:  "Must be defined inside a Class",
	1049:  "Expected Let or Set or Get in property declaration",
	1050:  "Expected 'Property'",
	1051:  "Number of arguments must be consistent across properties specification",
	1052:  "Cannot have multiple default property/method in a Class",
	1053:  "Class initialize or terminate do not have arguments",
	1054:  "Property set or let must have at least one argument",
	1055:  "Unexpected 'Next'",
	1056:  "'Default' can be specified only on 'Property' or 'Function' or 'Sub'",
	1057:  "'Default' specification must also specify 'Public'",
	1058:  "'Default' specification can only be on Property Get",
	5016:  "Regular Expression object expected",
	5017:  "Syntax error in regular expression",
	5018:  "Unexpected quantifier",
	5019:  "Expected ']' in regular expression",
	5020:  "Expected ')' in regular expression",
	5021:  "Invalid range in character set",
	32811: "Element not found",
}

// errText returns the standard VBScript description for an error number.
func errText(num int) string {
	if s, ok := errMessages[num]; ok {
		return s
	}
	return "Unknown runtime error"
}

// newErr builds a runtime error carrying the standard message for num. A
// non-empty detail is appended the way VBScript does: "Type mismatch: 'x'".
func newErr(num int, detail string) *Error {
	e := &Error{Number: num, Message: errText(num)}
	if detail != "" {
		e.Message += ": '" + detail + "'"
	} else {
		e.bare = true
	}
	return e
}

// newErrMsg builds a runtime error with a free-form message.
func newErrMsg(num int, msg string) *Error {
	return &Error{Number: num, Message: msg}
}

// withDetail appends a name to a bare standard error message. Errors that
// already carry a source position were raised by script code further down
// and are left alone.
func withDetail(err error, name string) error {
	if e, ok := err.(*Error); ok && e.bare && e.Line == 0 && name != "" {
		e.Message += ": '" + name + "'"
		e.bare = false
	}
	return err
}
