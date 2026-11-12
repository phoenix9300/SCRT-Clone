# $language = "Python"
# Log in to a device if it asks for a login, then run a command.
# Run it with Script > Run... on a connected tab.
# Optional arguments: username password (otherwise you are asked when needed).
screen = crt.Screen
screen.Synchronous = True


def argument(i, question, is_password=False):
    if crt.Arguments.Count > i:
        return crt.Arguments[i]
    return crt.Dialog.Prompt(question, "Login", "" if is_password else "admin", is_password)


# Press Enter so that the device shows its current prompt, then see what it is.
screen.Send("\r")
which = screen.WaitForStrings(["login:", "Username:", "$ ", "# ", "> "], 30)
if which == 0:
    crt.Dialog.MessageBox("The device showed no login or shell prompt.", "Login")
    crt.Quit()

if which in (1, 2):
    user = argument(0, "User name:")
    screen.Send(user + "\r")
    if screen.WaitForString("assword:", 15):
        screen.Send(argument(1, "Password:", True) + "\r")
    screen.WaitForStrings(["$ ", "# ", "> "], 30)
    who = "logged in as " + user
else:
    who = "already logged in"

# Use a marker the echo of the command line cannot contain.
screen.Send("echo hawser-$((6*7))\r")
if screen.WaitForString("hawser-42", 10):
    print("Done: " + who)
else:
    print("The command got no answer.")
