# clishake — one command opens an empty dashboard; add agents right there
Output docs/assets/quickstart.gif
Set Shell "bash"
Set FontSize 15
Set Width 1900
Set Height 1120
Set Padding 16
Set Theme { "name": "clishake", "background": "#12131a", "foreground": "#d4dcea", "cursor": "#ff87ff", "black": "#12131a", "red": "#ff6b6b", "green": "#5fd7af", "yellow": "#ffd787", "blue": "#5fd7ff", "magenta": "#ff87ff", "cyan": "#5fd7ff", "white": "#d4dcea", "brightBlack": "#8b95a7", "brightGreen": "#5fd7af", "brightMagenta": "#ff87ff", "brightCyan": "#5fd7ff" }
Set WindowBar Colorful

Hide
Type "export PATH=$HOME/.local/bin:/opt/homebrew/bin:/usr/local/bin:$PATH; clear" Enter
Sleep 400ms
Show
Type "clishake init"
Sleep 900ms
Enter
Sleep 4s
Type "/add codex codex reviewer"
Sleep 900ms
Enter
Sleep 3s
Type "/add claude claude-code builder"
Sleep 900ms
Enter
Sleep 5s
