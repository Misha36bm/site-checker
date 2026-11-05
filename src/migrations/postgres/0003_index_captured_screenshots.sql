-- Finds the check that captured a screenshot, for checks reusing that file.
CREATE INDEX idx_checks_captured_screenshot
    ON checks (screenshot_path, screenshot_captured_at DESC, id DESC)
    WHERE browser_final_url IS NOT NULL;
