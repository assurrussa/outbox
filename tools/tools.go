//go:build tools

// Данный пакет содержит инструменты для сборки проекта.
package tools

// Директива go:generate указывает, какую команду нужно выполнить.
// Мы запускаем нашу программу из папки tools/mocks.
// go:generate toolsmocks

import (
	// Импортируем mockgen, чтобы он был зафиксирован в go.mod как зависимость.
	_ "go.uber.org/mock/mockgen"
)
