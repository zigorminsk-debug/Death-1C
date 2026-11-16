; ---------------------------------------------------------------------------
;  Установщик программы «Очистка сеансов 1С» (Session Cleaner for 1C)
;  Разработчик: Zakharevich Igor, ziv@csl.by, +375293371412
;
;  Сборка выполняется в GitHub Actions:
;    makensis -DAPP_VERSION=1.0.0 -DBUILD_NUMBER=<порядковый номер> \
;             -DOUT_DIR=..\out installer\setup.nsi
; ---------------------------------------------------------------------------

!include "MUI2.nsh"
!include "x64.nsh"
!include "FileFunc.nsh"

!ifndef APP_VERSION
  !define APP_VERSION "1.0.0"
!endif
!ifndef BUILD_NUMBER
  !define BUILD_NUMBER "0"
!endif
!ifndef OUT_DIR
  ; Путь относительно каталога этого файла; в GitHub Actions передаётся
  ; абсолютный путь через -DOUT_DIR=...
  !define OUT_DIR "..\..\build\out"
!endif

Name "Очистка сеансов 1С — сборка ${BUILD_NUMBER}"
Caption "Установка «Очистка сеансов 1С» (сборка ${BUILD_NUMBER})"
UninstallCaption "Удаление «Очистка сеансов 1С»"
OutFile "${OUT_DIR}\SessionCleaner-Setup-${BUILD_NUMBER}.exe"

InstallDir "$PROGRAMFILES64\1C Session Cleaner"
InstallDirRegKey HKLM "Software\1C Session Cleaner" "InstallDir"

; Установщик обязан работать с правами администратора: он пишет в
; Program Files и создаёт ярлык, который всегда запускает программу
; от имени администратора.
RequestExecutionLevel admin

SetCompressor /SOLID lzma
Unicode true
BrandingText "Zakharevich Igor · ziv@csl.by · +375293371412"

; ------------------------------- MUI ---------------------------------------
!define MUI_ICON "..\assets\icon\app.ico"
!define MUI_UNICON "..\assets\icon\app.ico"
!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "$INSTDIR\SessionCleaner.exe"
!define MUI_FINISHPAGE_RUN_TEXT "Запустить программу сейчас"
!define MUI_FINISHPAGE_SHOWREADME "$INSTDIR\README.txt"
!define MUI_FINISHPAGE_SHOWREADME_TEXT "Показать памятку администратору"
!define MUI_WELCOMEFINISHPAGE_BITMAP "..\assets\icon\welcome.bmp"
!define MUI_HEADERIMAGE
!define MUI_HEADERIMAGE_BITMAP "..\assets\icon\header.bmp"
!define MUI_FINISHPAGE_LINK "Написать разработчику: ziv@csl.by"
!define MUI_FINISHPAGE_LINK_URL "mailto:ziv@csl.by?subject=Очистка сеансов 1С, сборка ${BUILD_NUMBER}"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_COMPONENTS
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "Russian"

; ------------------------------- Секции ------------------------------------
Section "Программа «Очистка сеансов 1С»" SEC_MAIN
  SectionIn RO
  SetOutPath "$INSTDIR"

  File "..\build\out\SessionCleaner.exe"
  File /oname=README.txt "..\installer\README.txt"

  WriteUninstaller "$INSTDIR\Uninstall.exe"

  ; Ярлык в меню «Пуск».
  CreateDirectory "$SMPROGRAMS\Очистка сеансов 1С"
  CreateShortCut "$SMPROGRAMS\Очистка сеансов 1С\Очистка сеансов 1С.lnk" \
    "$INSTDIR\SessionCleaner.exe" "" "$INSTDIR\SessionCleaner.exe" 0

  ; Ярлык «Настройки» — программа стартует с меню настроек (то же самое,
  ; что запуск с удерживаемым Shift).
  CreateShortCut "$SMPROGRAMS\Очистка сеансов 1С\Настройки (ввести учётные данные).lnk" \
    "$INSTDIR\SessionCleaner.exe" "-settings" "$INSTDIR\SessionCleaner.exe" 0

  CreateShortCut "$SMPROGRAMS\Очистка сеансов 1С\Удалить программу.lnk" \
    "$INSTDIR\Uninstall.exe"

  ; Ярлык на рабочем столе: программа всегда стартует с правами
  ; администратора — это гарантирует манифест exe-файла.
  CreateShortCut "$DESKTOP\Очистка сеансов 1С.lnk" \
    "$INSTDIR\SessionCleaner.exe" "" "$INSTDIR\SessionCleaner.exe" 0

  ; Сведения для «Установка и удаление программ».
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\1CSessionCleaner" \
    "DisplayName" "Очистка сеансов 1С (сборка ${BUILD_NUMBER})"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\1CSessionCleaner" \
    "DisplayVersion" "${APP_VERSION}.${BUILD_NUMBER}"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\1CSessionCleaner" \
    "Publisher" "Zakharevich Igor"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\1CSessionCleaner" \
    "HelpLink" "mailto:ziv@csl.by"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\1CSessionCleaner" \
    "Contact" "ziv@csl.by, +375293371412"
  ; Путь обязан быть в кавычках: без них оболочка спотыкается о пробел
  ; в «Program Files» и uninstall не запускается («нет доступа к файлу»).
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\1CSessionCleaner" \
    "UninstallString" "\"$INSTDIR\Uninstall.exe\""
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\1CSessionCleaner" \
    "QuietUninstallString" "\"$INSTDIR\Uninstall.exe\" /S"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\1CSessionCleaner" \
    "InstallLocation" "$INSTDIR"
  WriteRegDWORD HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\1CSessionCleaner" \
    "NoModify" 1
  WriteRegDWORD HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\1CSessionCleaner" \
    "NoRepair" 1
  WriteRegStr HKLM "Software\1C Session Cleaner" "InstallDir" "$INSTDIR"
  WriteRegStr HKLM "Software\1C Session Cleaner" "Build" "${BUILD_NUMBER}"

  ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
  IntFmt $0 "0x%08X" $0
  WriteRegDWORD HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\1CSessionCleaner" \
    "EstimatedSize" $0
SectionEnd

Section "Автоочистка по расписанию (ежедневно в 02:00)" SEC_TASK
  ; Задание планировщика: консольный режим без окна, от имени SYSTEM.
  nsExec::ExecToLog 'schtasks /Create /TN "1CSessionCleanerNightly" /TR "\"$INSTDIR\SessionCleaner.exe\" -clean -yes" /SC DAILY /ST 02:00 /RU SYSTEM /RL HIGHEST /F'
  Pop $0
SectionEnd

Section -Post
  SetOutPath "$INSTDIR"
  WriteIniStr "$INSTDIR\developer.url" "InternetShortcut" "URL" "mailto:ziv@csl.by"
SectionEnd

!insertmacro MUI_FUNCTION_DESCRIPTION_BEGIN
  !insertmacro MUI_DESCRIPTION_TEXT ${SEC_MAIN} \
    "Сама программа, ярлыки в меню «Пуск» и на рабочем столе, запись в списке установленных программ."
  !insertmacro MUI_DESCRIPTION_TEXT ${SEC_TASK} \
    "Задание планировщика Windows: каждую ночь в 02:00 программа без окна завершает старые сеансы и пишет отчёт в журнал."
!insertmacro MUI_FUNCTION_DESCRIPTION_END

; ------------------------------- Удаление ----------------------------------
Section "Uninstall"
  SetShellVarContext all
  nsExec::ExecToLog 'schtasks /Delete /TN "1CSessionCleanerNightly" /F'
  Pop $0

  Delete "$DESKTOP\Очистка сеансов 1С.lnk"
  Delete "$SMPROGRAMS\Очистка сеансов 1С\Очистка сеансов 1С.lnk"
  Delete "$SMPROGRAMS\Очистка сеансов 1С\Настройки (ввести учётные данные).lnk"
  Delete "$SMPROGRAMS\Очистка сеансов 1С\Удалить программу.lnk"
  RMDir "$SMPROGRAMS\Очистка сеансов 1С"

  Delete "$INSTDIR\SessionCleaner.exe"
  Delete "$INSTDIR\Uninstall.exe"
  Delete "$INSTDIR\README.txt"
  Delete "$INSTDIR\developer.url"
  RMDir "$INSTDIR"

  DeleteRegKey HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\1CSessionCleaner"
  DeleteRegKey HKLM "Software\1C Session Cleaner"

  ; Настройки и зашифрованные учётные данные лежат в %ProgramData% и
  ; удаляются только по явному желанию администратора.
  MessageBox MB_YESNO|MB_ICONQUESTION \
    "Удалить также сохранённые настройки и зашифрованные учётные данные?$\r$\n(%ProgramData%\1C Session Cleaner)" \
    IDNO keep_data
  RMDir /r "$PROGRAMDATA\1C Session Cleaner"
keep_data:
SectionEnd

; ------------------------------- Проверки ----------------------------------
Function .onInit
  ${If} ${RunningX64}
    ; всё хорошо: 64-битная Windows
  ${Else}
    MessageBox MB_OK|MB_ICONSTOP "Программа собирается только для 64-битной Windows."
    Quit
  ${EndIf}

  ; Не даём ставить поверх работающей копии.
  check_running:
  nsExec::ExecToStack 'tasklist /FI "IMAGENAME eq SessionCleaner.exe" /NH'
  Pop $0
  Pop $1
  StrCpy $2 $1 24
  ${If} $2 == "SessionCleaner.exe"
    MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION \
      "Программа «Очистка сеансов 1С» сейчас запущена.$\r$\nЗакройте её и повторите попытку." IDCANCEL cancel_install
    Goto check_running
  ${EndIf}
  Goto done_init
cancel_install:
  Quit
done_init:
FunctionEnd

VIProductVersion "${APP_VERSION}.${BUILD_NUMBER}"
VIAddVersionKey /LANG=1049 "ProductName" "Очистка сеансов 1С"
VIAddVersionKey /LANG=1049 "ProductVersion" "${APP_VERSION}"
VIAddVersionKey /LANG=1049 "FileVersion" "${APP_VERSION}.${BUILD_NUMBER}"
VIAddVersionKey /LANG=1049 "LegalCopyright" "© Zakharevich Igor. Все права защищены."
VIAddVersionKey /LANG=1049 "FileDescription" "Установщик программы «Очистка сеансов 1С»"
VIAddVersionKey /LANG=1049 "CompanyName" "Zakharevich Igor"
VIAddVersionKey /LANG=1049 "Comments" "Разработчик: Zakharevich Igor, ziv@csl.by, +375293371412"
