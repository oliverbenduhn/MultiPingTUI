# Analyse: Was hat sich zwischen funktionierender und nicht-funktionierender Version geändert?

## Funktionierende Version (2c3215c)
```go
func RunTUI(wh *WrapperHolder, tw *TransitionWriter, initialFilter FilterMode) error {
    model := NewTUIModel(wh, tw, initialFilter)
    p := tea.NewProgram(model, tea.WithAltScreen())
    
    wh.Start()          // ← BLOCKIERT HIER BIS ALLE WRAPPER FERTIG
    defer wh.Stop()
    
    _, err := p.Run()   // ← TUI STARTET ERST DANACH
    return err
}
```

## Nicht-funktionierende Version (backup-perf-fixes)
```go
func RunTUI(...) (finalErr error) {
    // Panic protection
    defer func() { ... }()
    
    // Start wrappers in Goroutine mit Timeout
    startDone := make(chan bool, 1)
    go func() {
        wh.Start()      // ← IN GOROUTINE!
        startDone <- true
    }()
    
    // Warte auf Start
    select {
    case <-startDone:
        // OK
    case <-time.After(60s):
        return timeout
    }
    
    defer wh.Stop()
    
    model := NewTUIModel(wh, tw, initialFilter)
    p := tea.NewProgram(model, tea.WithAltScreen())
    
    _, err := p.Run()
    return err
}
```

## HAUPTUNTERSCHIED #1: wh.Start() Timing

**FUNKTIONIEREND:**
- `wh.Start()` wird **synchron** aufgerufen
- **BLOCKIERT** bis alle 254 Wrapper gestartet sind
- TUI startet erst danach → Stats-Cache ist LEER beim ersten View()

**NICHT FUNKTIONIEREND:**
- `wh.Start()` läuft in **Goroutine**
- Wartet nur auf Signal, nicht auf tatsächliche Fertigstellung
- TUI startet "gleichzeitig" → Stats-Cache ist LEER beim ersten View()
- **PROBLEM**: Stats-Cache wird nie initial befüllt!

## HAUPTUNTERSCHIED #2: Stats-Cache

**FUNKTIONIEREND (2c3215c):**
```go
case tickMsg:
    m.wh.CalcStats(2 * 1e9)  // Berechnet direkt
    return m, tickCmd()
```

**NICHT FUNKTIONIEREND (backup-perf-fixes):**
```go
case tickMsg:
    m.updateStatsCache()     // Befüllt Cache
    return m, tickCmd()

// ABER: Erster View() kommt VOR erstem tickMsg!
```

## DAS PROBLEM

1. TUI startet → `Init()` wird aufgerufen
2. `Init()` sendet ersten `tickCmd()` (100ms delay)
3. **SOFORT DANACH**: `View()` wird aufgerufen (erste Render)
4. `View()` → `getFilteredWrappers()` → `getCachedStats()`
5. **Cache ist LEER!** → `CalcStats()` wird aufgerufen
6. Bei 254 Hosts × Sortierung = **TAUSENDE CalcStats() Aufrufe**
7. **TUI BLOCKIERT KOMPLETT**

## LÖSUNG

Der Stats-Cache muss **VOR** dem ersten View() befüllt werden:

```go
func NewTUIModel(...) *TUIModel {
    m := &TUIModel{
        // ...
        statsCache: make(map[string]PWStats),
    }
    
    // ⚠️ WICHTIG: Cache initial befüllen!
    m.updateStatsCache()
    
    return m
}
```

ODER im `Init()`:
```go
func (m *TUIModel) Init() tea.Cmd {
    // Cache initial befüllen
    m.updateStatsCache()
    
    return tea.Batch(
        tickCmd(),
        tea.EnterAltScreen,
    )
}
```

## WEITERE UNTERSCHIEDE

1. **Spalten-Toggles** (1-6 Tasten): Ändert Rendering-Logik
2. **Verbesserte Farben**: Von numerisch zu Hex-Werten
3. **Separator-Style**: Neuer Style hinzugefügt
4. **Column-Visibility**: Komplexere Rendering-Logik

## WARUM FUNKTIONIERT DIE ALTE VERSION?

In der alten Version:
- Kein Cache
- `CalcStats()` wird bei jedem View() aufgerufen
- **ABER**: Ohne komplexe Column-Visibility-Logik
- Simpler Render-Code = weniger Aufrufe
- Trotzdem schlecht, aber nicht SO schlecht dass es hängt

## FAZIT

Das Problem ist **NICHT** die Panic-Protection oder Timeout-Logik.
Das Problem ist die **Kombination** aus:
1. Stats-Cache der nicht initial befüllt wird
2. Komplexerer Rendering-Code mit mehr CalcStats-Aufrufen
3. Fehlender initialer Cache-Fill vor erstem View()
