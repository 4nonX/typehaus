; typehaus for Windows – per-user installer (no administrator rights).
;
; Installs to %LOCALAPPDATA%\Programs\typehaus, adds a Start menu entry and
; an entry under Settings → Apps (with uninstaller), and starts typehaus at
; login. One installer for Intel/AMD and ARM: the matching build is chosen
; at install time.
;
; Silent:     typehaus-setup.exe /S           (deployment via Intune, GPO, …)
; Uninstall:  Settings → Apps, or "%LOCALAPPDATA%\Programs\typehaus\uninstall.exe" [/S]
;
; Built on Linux or Windows with NSIS 3.08+:
;   makensis -DVERSION=0.3.1 -DSRC=out/windows -DOUTFILE=dist/typehaus-setup.exe packaging/windows/typehaus.nsi
; SRC must contain typehaus-agent-windows-{amd64,arm64}.exe and
; typehaus-agentw-windows-{amd64,arm64}.exe.

Unicode true
SetCompressor /SOLID lzma
RequestExecutionLevel user
ManifestDPIAware true

!ifndef VERSION
  !define VERSION "0.0.0"
!endif
!ifndef SRC
  !define SRC "out\windows"
!endif
!ifndef OUTFILE
  !define OUTFILE "typehaus-setup.exe"
!endif

; numeric x.y.z.0 for the version resource (strip "-rc1" style suffixes)
!searchparse /noerrors "${VERSION}" "" V_MAJ "." V_MIN "." V_PAT
!ifndef V_MAJ
  !define V_MAJ 0
!endif
!ifndef V_MIN
  !define V_MIN 0
!endif
!ifndef V_PAT
  !define V_PAT 0
!endif
!searchparse /noerrors "${V_PAT}" "" V_PATN "-"
!ifndef V_PATN
  !define V_PATN ${V_PAT}
!endif

!define UNINST "Software\Microsoft\Windows\CurrentVersion\Uninstall\typehaus"
!define RUNKEY "Software\Microsoft\Windows\CurrentVersion\Run"

Name "typehaus"
OutFile "${OUTFILE}"
InstallDir "$LOCALAPPDATA\Programs\typehaus"
BrandingText "typehaus ${VERSION}"

VIProductVersion "${V_MAJ}.${V_MIN}.${V_PATN}.0"
VIAddVersionKey /LANG=0 "ProductName" "typehaus"
VIAddVersionKey /LANG=0 "ProductVersion" "${VERSION}"
VIAddVersionKey /LANG=0 "FileVersion" "${VERSION}"
VIAddVersionKey /LANG=0 "FileDescription" "typehaus setup"
VIAddVersionKey /LANG=0 "CompanyName" "typehaus contributors"
VIAddVersionKey /LANG=0 "LegalCopyright" "AGPL-3.0"

!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "x64.nsh"
!include "FileFunc.nsh"

!define MUI_ICON "..\..\cmd\typehaus-agent\winres\icon.ico"
!define MUI_UNICON "..\..\cmd\typehaus-agent\winres\icon.ico"
!define MUI_ABORTWARNING

!define MUI_WELCOMEPAGE_TEXT "$(WelcomeText)"
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_RUN "$INSTDIR\typehaus.exe"
!define MUI_FINISHPAGE_RUN_TEXT "$(RunText)"
!insertmacro MUI_PAGE_FINISH

!define MUI_UNCONFIRMPAGE_TEXT_TOP "$(UninstText)"
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"
!insertmacro MUI_LANGUAGE "German"

LangString WelcomeText ${LANG_ENGLISH} "This installs typehaus ${VERSION} for your user account – no administrator rights needed.$\r$\n$\r$\ntypehaus keeps the fonts you activate in your team's font library installed on this computer.$\r$\n$\r$\nIf typehaus is running, it is closed and started again after the update. Your fonts stay installed."
LangString WelcomeText ${LANG_GERMAN} "Installiert typehaus ${VERSION} für Ihr Benutzerkonto – ohne Administratorrechte.$\r$\n$\r$\ntypehaus hält die Schriften, die Sie in der Schriftbibliothek Ihres Teams aktivieren, auf diesem Computer installiert.$\r$\n$\r$\nLäuft typehaus gerade, wird es beendet und nach dem Update neu gestartet. Ihre Schriften bleiben installiert."
LangString RunText ${LANG_ENGLISH} "Start typehaus"
LangString RunText ${LANG_GERMAN} "typehaus starten"
LangString UninstText ${LANG_ENGLISH} "typehaus will be removed from this computer, together with the fonts it installed. Fonts stay in your typehaus library and can be activated again at any time."
LangString UninstText ${LANG_GERMAN} "typehaus wird von diesem Computer entfernt, zusammen mit den Schriften, die es installiert hat. Die Schriften bleiben in Ihrer typehaus-Bibliothek und lassen sich jederzeit wieder aktivieren."
LangString Need64 ${LANG_ENGLISH} "typehaus needs a 64-bit version of Windows 10 or newer."
LangString Need64 ${LANG_GERMAN} "typehaus benötigt eine 64-Bit-Version von Windows 10 oder neuer."

Var Fresh ; 1 = not installed before

Function .onInit
  ${IfNot} ${RunningX64}
    MessageBox MB_ICONSTOP "$(Need64)" /SD IDOK
    Abort
  ${EndIf}
FunctionEnd

Section "typehaus" SecMain
  SectionIn RO

  ; Was typehaus installed or used here before? (keeps the user's
  ; "Start at login" choice on updates)
  StrCpy $Fresh 1
  ReadRegStr $0 HKCU "${UNINST}" "InstallLocation"
  ${If} $0 != ""
    StrCpy $Fresh 0
  ${EndIf}
  ${If} ${FileExists} "$APPDATA\typehaus\agent.json"
    StrCpy $Fresh 0
  ${EndIf}
  ReadRegStr $1 HKCU "${RUNKEY}" "typehaus"
  ${If} $1 == ""
    ReadRegStr $1 HKCU "${RUNKEY}" "typehaus-agent" ; v0.1
  ${EndIf}

  ; Close a running typehaus (wherever it was started from) so its files
  ; can be replaced. The new build asks it to quit gracefully.
  InitPluginsDir
  ${If} ${IsNativeARM64}
    File "/oname=$PLUGINSDIR\th.exe" "${SRC}\typehaus-agent-windows-arm64.exe"
  ${Else}
    File "/oname=$PLUGINSDIR\th.exe" "${SRC}\typehaus-agent-windows-amd64.exe"
  ${EndIf}
  nsExec::Exec '"$PLUGINSDIR\th.exe" quit'
  Pop $0

  SetOutPath "$INSTDIR"
  ${If} ${IsNativeARM64}
    File "/oname=typehaus.exe" "${SRC}\typehaus-agentw-windows-arm64.exe"
    File "/oname=typehaus-agent.exe" "${SRC}\typehaus-agent-windows-arm64.exe"
  ${Else}
    File "/oname=typehaus.exe" "${SRC}\typehaus-agentw-windows-amd64.exe"
    File "/oname=typehaus-agent.exe" "${SRC}\typehaus-agent-windows-amd64.exe"
  ${EndIf}
  WriteUninstaller "$INSTDIR\uninstall.exe"

  ; Start menu
  CreateShortcut "$SMPROGRAMS\typehaus.lnk" "$INSTDIR\typehaus.exe" "" "$INSTDIR\typehaus.exe" 0

  ; Settings → Apps
  WriteRegStr HKCU "${UNINST}" "DisplayName" "typehaus"
  WriteRegStr HKCU "${UNINST}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINST}" "Publisher" "typehaus contributors"
  WriteRegStr HKCU "${UNINST}" "DisplayIcon" "$INSTDIR\typehaus.exe,0"
  WriteRegStr HKCU "${UNINST}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINST}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegStr HKCU "${UNINST}" "QuietUninstallString" '"$INSTDIR\uninstall.exe" /S'
  WriteRegStr HKCU "${UNINST}" "URLInfoAbout" "https://github.com/4nonX/typehaus"
  WriteRegDWORD HKCU "${UNINST}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINST}" "NoRepair" 1
  ${GetSize} "$INSTDIR" "/S=0K" $0 $2 $3
  IntFmt $0 "0x%08X" $0
  WriteRegDWORD HKCU "${UNINST}" "EstimatedSize" "$0"

  ; Start at login: on for new installs; on updates as it was before,
  ; now pointing at the installed program
  DeleteRegValue HKCU "${RUNKEY}" "typehaus-agent"
  ${If} $Fresh == 1
  ${OrIf} $1 != ""
    WriteRegStr HKCU "${RUNKEY}" "typehaus" '"$INSTDIR\typehaus.exe" tray --autostart'
  ${EndIf}

  ; copies older versions kept for autostart
  RMDir /r "$APPDATA\typehaus\bin"
SectionEnd

; silent installs (deployment) start typehaus right away when it is set to
; start at login; interactive ones offer it on the last page
Function .onInstSuccess
  ${If} ${Silent}
    ReadRegStr $0 HKCU "${RUNKEY}" "typehaus"
    ${If} $0 != ""
      Exec '"$INSTDIR\typehaus.exe" tray --autostart'
    ${EndIf}
  ${EndIf}
FunctionEnd

Section "Uninstall"
  ; quits typehaus, removes the fonts it installed, its autostart entry and
  ; the connection to the server
  nsExec::Exec '"$INSTDIR\typehaus-agent.exe" unenroll'
  Pop $0
  nsExec::Exec '"$INSTDIR\typehaus-agent.exe" quit'
  Pop $0
  DeleteRegValue HKCU "${RUNKEY}" "typehaus"
  DeleteRegValue HKCU "${RUNKEY}" "typehaus-agent"

  Delete "$SMPROGRAMS\typehaus.lnk"
  Delete "$INSTDIR\typehaus.exe"
  Delete "$INSTDIR\typehaus-agent.exe"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"
  DeleteRegKey HKCU "${UNINST}"
SectionEnd
