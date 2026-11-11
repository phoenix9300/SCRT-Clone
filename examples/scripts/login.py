# $language = "Python"
# Log in to a device and run a command. Run it from Script > Run... on a connected tab.
# Optional arguments: username password
user = crt.Arguments[0] if crt.Arguments.Count > 0 else crt.Dialog.Prompt("User name:", "Login", "admin")
password = crt.Arguments[1] if crt.Arguments.Count > 1 else crt.Dialog.Prompt("Password:", "Login", "", True)

screen = crt.Screen
screen.Synchronous = True

i = screen.WaitForStrings(["login:", "Username:", "$ ", "# "], 30)
if i in (1, 2):
    screen.Send(user + "\r")
    if screen.WaitForString("assword:", 15):
        screen.Send(password + "\r")
elif i == 0:
    crt.Dialog.MessageBox("No login prompt appeared.", "Login")
    crt.Quit()

screen.WaitForStrings(["$ ", "# ", "> "], 30)
screen.Send("uptime\r")
screen.WaitForString("load average", 10)
print("Logged in as", user)
