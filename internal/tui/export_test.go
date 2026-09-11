package tui

import "gemsub/internal/tui/viewmodel"

// SourceInputFocusForTest returns the active input focus index (0 for URL, 1 for Name)
// within the source add/edit modal for tests.
func (m *Model) SourceInputFocusForTest() int {
	return m.inputFocus
}

// SourceModeForTest returns the current sourceInputMode as an int for tests.
func (m *Model) SourceModeForTest() int {
	return int(m.sourceMode)
}

// RenderFooterForTest exposes renderFooter for testing terminal-width truncation.
func (m *Model) RenderFooterForTest() string {
	return m.renderFooter()
}

// SetStatusMessageForTest sets a status message on Model for testing.
func (m *Model) SetStatusMessageForTest(msg string) {
	m.statusMessage = msg
}

// SetWidthForTest sets the model terminal width for testing.
func (m *Model) SetWidthForTest(w int) {
	m.width = w
}

// TruncateToWidthForTest exposes truncateToWidth for testing.
func TruncateToWidthForTest(s string, maxWidth int) string {
	return truncateToWidth(s, maxWidth)
}

// RenderSourceManagerForTest exposes renderSourceManager for testing.
func (m *Model) RenderSourceManagerForTest() string {
	return m.renderSourceManager()
}

// SetSourceModeForTest sets sourceMode for testing.
func (m *Model) SetSourceModeForTest(mode int) {
	m.sourceMode = sourceInputMode(mode)
}

// SetSourceInputURLForTest sets inputURL for testing.
func (m *Model) SetSourceInputURLForTest(u string) {
	m.inputURL = u
}

// SetSourceInputNameForTest sets inputName for testing.
func (m *Model) SetSourceInputNameForTest(n string) {
	m.inputName = n
}

// SourceInputURLForTest returns inputURL for testing.
func (m *Model) SourceInputURLForTest() string {
	return m.inputURL
}

// SourceInputNameForTest returns inputName for testing.
func (m *Model) SourceInputNameForTest() string {
	return m.inputName
}

// SetConfirmDeleteNameForTest sets confirmDeleteName for testing.
func (m *Model) SetConfirmDeleteNameForTest(n string) {
	m.confirmDeleteName = n
}

// SetSourceInputFocusForTest sets inputFocus for testing.
func (m *Model) SetSourceInputFocusForTest(f int) {
	m.inputFocus = f
}

const (
	SourceModeNormal        = int(sourceModeNormal)
	SourceModeDeleteConfirm = int(sourceModeDeleteConfirm)
	SourceModeAdd           = int(sourceModeAdd)
	SourceModeEdit          = int(sourceModeEdit)
)

// SettingEditModeForTest returns settingEditMode for tests.
func (m *Model) SettingEditModeForTest() bool {
	return m.settingEditMode
}

// SetSettingEditModeForTest sets settingEditMode for tests.
func (m *Model) SetSettingEditModeForTest(edit bool) {
	m.settingEditMode = edit
}

// SettingKeyForTest returns settingKey for tests.
func (m *Model) SettingKeyForTest() string {
	return m.settingKey
}

// SettingLabelForTest returns settingLabel for tests.
func (m *Model) SettingLabelForTest() string {
	return m.settingLabel
}

// SettingInputValForTest returns settingInputVal for tests.
func (m *Model) SettingInputValForTest() string {
	return m.settingInputVal
}

// SetSettingInputValForTest sets settingInputVal for tests.
func (m *Model) SetSettingInputValForTest(v string) {
	m.settingInputVal = v
}

// SetSettingKeyForTest sets settingKey for tests.
func (m *Model) SetSettingKeyForTest(k string) {
	m.settingKey = k
}

// SetSettingLabelForTest sets settingLabel for tests.
func (m *Model) SetSettingLabelForTest(l string) {
	m.settingLabel = l
}

// SetSettingRestartRequiredForTest sets settingRestartRequired for tests.
func (m *Model) SetSettingRestartRequiredForTest(r bool) {
	m.settingRestartRequired = r
}

// SetSettingDescriptionForTest sets settingDescription for tests.
func (m *Model) SetSettingDescriptionForTest(d string) {
	m.settingDescription = d
}

// RenderSettingEditorModalForTest exposes renderSettingEditorModal for tests.
func (m *Model) RenderSettingEditorModalForTest() string {
	return m.renderSettingEditorModal()
}

// RenderCategoryItemsForTest exposes renderCategoryItems for tests.
func (m *Model) RenderCategoryItemsForTest(items []viewmodel.ConfigItemViewModel) string {
	return m.renderCategoryItems(items)
}

// ConfigItemIndexForTest returns configItemIndex for tests.
func (m *Model) ConfigItemIndexForTest() int {
	return m.configItemIndex
}

// SetConfigItemIndexForTest sets configItemIndex for tests.
func (m *Model) SetConfigItemIndexForTest(idx int) {
	m.configItemIndex = idx
}

// ConfigCategoryForTest returns configCategory for tests.
func (m *Model) ConfigCategoryForTest() int {
	return int(m.configCategory)
}

// SetConfigCategoryForTest sets configCategory for tests.
func (m *Model) SetConfigCategoryForTest(cat int) {
	m.configCategory = viewmodel.ConfigCategory(cat)
}
