package postgres

import (
	"context"
	"strings"

	"fuck-the-bot/internal/model"
)

func (s *Store) FindContacts(ctx context.Context, chatID int64, query string) ([]model.Contact, error) {
	query = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(query), "@"))
	if query == "" {
		return nil, nil
	}
	query = strings.ReplaceAll(strings.ToLower(query), "ё", "е")
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	rows, err := s.pool.Query(queryCtx, `
		SELECT user_id, username, display_name, link
		FROM bot_contacts
		WHERE chat_id = $1 AND (
			lower(username) = $2 OR translate(lower(display_name), 'ё', 'е') = $2
			OR strpos(lower(username), $2) > 0
			OR strpos(translate(lower(display_name), 'ё', 'е'), $2) > 0
		)
		ORDER BY
			CASE WHEN lower(username) = $2 THEN 0
			     WHEN translate(lower(display_name), 'ё', 'е') = $2 THEN 1
			     WHEN left(lower(username), length($2)) = $2 THEN 2
			     ELSE 3 END,
			last_seen_at DESC, user_id
		LIMIT 6`, chatID, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var contacts []model.Contact
	for rows.Next() {
		var contact model.Contact
		if err := rows.Scan(&contact.UserID, &contact.Username, &contact.Name, &contact.Link); err != nil {
			return nil, err
		}
		contacts = append(contacts, contact)
	}
	return contacts, rows.Err()
}
