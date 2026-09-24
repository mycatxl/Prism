-- Irreversible: ANY and MUST_NOT rules cannot be represented by the legacy
-- all-regexes-must-match format. Restore a pre-upgrade state.db to downgrade.
UPDATE platforms
SET regex_filters_json = (
	SELECT COALESCE(json_group_array('*' || value), '[]')
	FROM json_each(platforms.regex_filters_json)
)
WHERE json_array_length(regex_filters_json) > 1;

UPDATE platforms
SET regex_filters_json = (
	SELECT json_group_array(char(92) || value)
	FROM json_each(platforms.regex_filters_json)
)
WHERE json_array_length(regex_filters_json) = 1
	AND substr(json_extract(regex_filters_json, '$[0]'), 1, 1) = '!';
